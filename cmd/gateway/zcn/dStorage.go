package zcn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	zerror "github.com/0chain/errors"
	"github.com/0chain/gosdk/constants"
	"github.com/0chain/gosdk/core/sys"
	"github.com/0chain/gosdk/zboxcore/sdk"
	"github.com/google/uuid"
	minio "github.com/minio/minio/cmd"
	"github.com/minio/minio/internal/logger"
)

var tempdir string

const (
	pageLimit = 100
	numBlocks = 100
	dirType   = "d"
	fileType  = "f"

	defaultChunkSize     = 64 * 1024
	maxSizeForMemoryFile = 16 * 1024 * 1024
	fiveHunderedKB       = 500 * 1024
	oneMB                = 1024 * 1024
	tenMB                = 10 * oneMB
	hundredMB            = 10 * tenMB
	oneGB                = 1024 * oneMB

	// Error codes
	pathDoesNotExist = "path_no_exist"
	consensusFailed  = "consensus_failed"
	retryWaitTime    = 500 * time.Millisecond // milliseconds
)

func init() {
	var err error
	tempdir, err = os.MkdirTemp("", "zcn*")
	if err != nil {
		panic(fmt.Sprintf("could not create tempdir. Error: %v", err))
	}
}

func listRootDir(alloc *sdk.Allocation, fileType string) ([]sdk.ORef, error) {
	var refs []sdk.ORef
	page := 1
	offsetPath := ""

	for {
		oResult, err := getRegularRefs(alloc, rootPath, offsetPath, fileType, pageLimit)
		if err != nil {

			return nil, err
		}

		refs = append(refs, oResult.Refs...)

		if page >= int(oResult.TotalPages) {
			break
		}

		page++
		offsetPath = oResult.OffsetPath
	}

	return refs, nil
}

func listRegularRefs(alloc *sdk.Allocation, remotePath, marker, fileType string, maxRefs int, isDelimited bool) ([]sdk.ORef, bool, string, []string, error) {
	var refs []sdk.ORef
	var prefixes []string
	var isTruncated bool
	var markedPath string

	remotePath = filepath.Clean(remotePath)

	directories := []string{remotePath}
	var currentRemotePath string
	for len(directories) > 0 && !isTruncated {
		currentRemotePath = directories[0]
		directories = directories[1:] // dequeue from the directories queue
		commonPrefix := getCommonPrefix(currentRemotePath)
		offsetPath := filepath.Join(currentRemotePath, marker)
		for {
			oResult, err := getRegularRefs(alloc, currentRemotePath, offsetPath, fileType, pageLimit)
			if err != nil {
				return nil, true, "", nil, err
			}
			if len(oResult.Refs) == 0 {
				break
			}

			for i := 0; i < len(oResult.Refs); i++ {
				ref := oResult.Refs[i]
				trimmedPath := strings.TrimPrefix(ref.Path, currentRemotePath+"/")
				if ref.Type == dirType {
					if isDelimited {
						dirPrefix := filepath.Join(commonPrefix, trimmedPath) + "/"
						prefixes = append(prefixes, dirPrefix)
						continue
					} else {
						directories = append(directories, ref.Path)
					}
				}

				ref.Name = filepath.Join(commonPrefix, trimmedPath)

				refs = append(refs, ref)
				if maxRefs != 0 && len(refs) >= maxRefs {
					markedPath = ref.Path
					isTruncated = true
					break
				}
			}
			offsetPath = oResult.OffsetPath
		}
	}
	if isTruncated {
		marker = strings.TrimPrefix(markedPath, currentRemotePath+"/")
	} else {
		marker = ""
	}

	return refs, isTruncated, marker, prefixes, nil
}

func getRegularRefs(alloc *sdk.Allocation, remotePath, offsetPath, fileType string, pageLimit int) (oResult *sdk.ObjectTreeResult, err error) {
	level := len(strings.Split(strings.TrimSuffix(remotePath, "/"), "/")) + 1
	remotePath = filepath.Clean(remotePath)
	oResult, err = alloc.GetRefs(remotePath, offsetPath, "", "", fileType, "regular", level, pageLimit)
	return
}

func getSingleRegularRef(alloc *sdk.Allocation, remotePath string) (*sdk.ORef, error) {
	level := len(strings.Split(strings.TrimSuffix(remotePath, "/"), "/"))
	remotePath = filepath.Clean(remotePath)
	oREsult, err := alloc.GetRefs(remotePath, "", "", "", "", "regular", level, 1)
	if err != nil {
		logger.Error("error with GetRefs", err.Error(), " this is the error")
		fmt.Println("error with GetRefs", err)
		if isConsensusFailedError(err) {
			time.Sleep(retryWaitTime)
			oREsult, err = alloc.GetRefs(remotePath, "", "", "", "", "regular", level, 1)
			if err != nil {
				//  alloc.GetRefs returns consensus error when the file doesn't exist
				if isConsensusFailedError(err) {
					return nil, zerror.New(pathDoesNotExist, fmt.Sprintf("remotepath %v does not exist", remotePath))
				}
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	if len(oREsult.Refs) == 0 {
		return nil, zerror.New(pathDoesNotExist, fmt.Sprintf("remotepath %v does not exist", remotePath))
	}

	return &oREsult.Refs[0], nil
}

var (
	mu sync.Mutex
)

func getObjectRef(alloc *sdk.Allocation, bucket, object, remotePath string) (*minio.ObjectInfo, bool, error) {
	log.Printf("~~~~~~~~~~~~~~~~~~~~~~~~ get object info remotePath: %v\n", remotePath)
	var isEncrypted bool
	ref, err := getSingleRegularRef(alloc, remotePath)
	if err != nil {
		if isPathNoExistError(err) {
			return nil, isEncrypted, minio.ObjectNotFound{Bucket: bucket, Object: object}
		}
		return nil, isEncrypted, err
	}
	if ref.EncryptedKey != "" {
		isEncrypted = true
	}

	log.Println("~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~get object info, ref: ", ref)

	return &minio.ObjectInfo{
		Bucket:      bucket,
		Name:        ref.Name,
		ModTime:     ref.UpdatedAt.ToTime(),
		Size:        ref.ActualFileSize,
		IsDir:       ref.Type == dirType,
		ContentType: ref.MimeType,
	}, isEncrypted, nil
}

func getFileReader(ctx context.Context,
	alloc *sdk.Allocation,
	bucket, object, remotePath string, rangeStart int64, rangeEnd int64) (io.Reader, *minio.ObjectInfo, func(), string, error) {
	downloadID := uuid.New().String()
	localFilePath := filepath.Join(tempdir, downloadID)
	fileRangeSize := rangeEnd - rangeStart + 1

	cb := statusCB{
		doneCh: make(chan struct{}, 1),
		errCh:  make(chan error, 1),
	}

	objectInfo, isEncrypted, err := getObjectRef(alloc, bucket, object, remotePath)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if rangeEnd < rangeStart {
		fileRangeSize = objectInfo.Size
	}
	var timeout time.Duration
	if objectInfo.ContentType == lz4MimeType {
		timeout = time.Minute * 30
	} else {
		timeout = getTimeOut(uint64(fileRangeSize))
	}
	var ctxCncl context.CancelFunc
	ctx, ctxCncl = context.WithTimeout(ctx, timeout)

	var startBlock, endBlock int64
	dataShards := int64(alloc.DataShards)
	effectiveBlockSize := int64(defaultChunkSize)
	if isEncrypted {
		effectiveBlockSize -= sdk.EncryptionHeaderSize + sdk.EncryptedDataPaddingSize
	}
	effectiveChunkSize := effectiveBlockSize * int64(dataShards)

	if rangeEnd >= rangeStart {
		startBlock = int64(rangeStart / effectiveChunkSize)
		if startBlock == 0 {
			startBlock = 1
		}
		endBlock = int64(rangeEnd) / effectiveChunkSize
	} else {
		startBlock = 1
		endBlock = 0
	}

	if rangeEnd == -1 {
		endBlock = 0
		startBlock = int64(rangeStart / effectiveChunkSize)
		if startBlock == 0 {
			startBlock = 1
		}
		fileRangeSize = objectInfo.Size - rangeStart
	}
	log.Println("^^^^^^^^getFileReader: starting download: ", startBlock, endBlock, rangeStart, rangeEnd, fileRangeSize)
	var r sys.File
	if startBlock == 1 && endBlock == 0 {
		log.Println("^^^^^^^^getFileReader: stream download ")
		pr, pw := io.Pipe()
		r = &pipeFile{w: pw}
		go func() {
			defer ctxCncl()
			err = alloc.DownloadByBlocksToFileHandler(r, remotePath, startBlock, endBlock, numBlocks, false, &cb, true)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			select {
			case <-cb.doneCh:
				pw.Close()
			case err := <-cb.errCh:
				pw.CloseWithError(err)
				return
			case <-ctx.Done():
				pw.CloseWithError(errors.New("exceeded timeout"))
				return
			}
		}()
		return pr, objectInfo, func() { pr.Close() }, localFilePath, nil
	} else {
		defer ctxCncl()
		if fileRangeSize > maxSizeForMemoryFile {
			r, err = os.Create(localFilePath)
			if err != nil {
				return nil, nil, nil, "", err
			}
		} else {
			localFilePath = ""
			r = &sys.MemFile{}
		}
	}
	err = alloc.DownloadByBlocksToFileHandler(r, remotePath, startBlock, endBlock, numBlocks, false, &cb, true)
	if err != nil {
		return nil, nil, nil, "", err
	}

	select {
	case <-cb.doneCh:
	case err := <-cb.errCh:
		return nil, nil, nil, "", err
	case <-ctx.Done():
		return nil, nil, nil, "", errors.New("exceeded timeout")
	}

	startOffset := rangeStart - startBlock*effectiveChunkSize
	if startOffset < 0 {
		startOffset = 0
	}
	_, err = r.Seek(startOffset, io.SeekStart)
	if err != nil {
		return nil, nil, nil, "", err
	}

	// create a new limited reader
	f := io.LimitReader(r, fileRangeSize)
	log.Println("^^^^^^^^getFileReader: finish download")
	fCloser := func() {
		r.Close() //nolint:errcheck
		if localFilePath != "" {
			os.Remove(localFilePath) // nolint:errcheck
		}
	}
	return f, objectInfo, fCloser, localFilePath, nil

}

func putFile(ctx context.Context, alloc *sdk.Allocation, remotePath, contentType string, r io.Reader, size int64, isUpdate, shouldEncrypt bool) (err error) {
	logger.Info("started PutFile")
	_, fileName := filepath.Split(remotePath)
	fileMeta := sdk.FileMeta{
		Path:       "",
		RemotePath: remotePath,
		ActualSize: size,
		MimeType:   contentType,
		RemoteName: fileName,
	}

	logger.Info("starting chunked upload")
	opRequest := sdk.OperationRequest{
		OperationType: constants.FileOperationInsert,
		FileReader:    newMinioReader(r),
		Workdir:       workDir,
		RemotePath:    remotePath,
		FileMeta:      fileMeta,
		Opts: []sdk.ChunkedUploadOption{
			sdk.WithChunkNumber(120),
			sdk.WithEncrypt(encrypt),
		},
	}
	if isUpdate {
		opRequest.OperationType = constants.FileOperationUpdate
	}
	err = alloc.DoMultiOperation([]sdk.OperationRequest{opRequest})
	if err != nil && !isSameRootError(err) {
		logger.Error(err.Error())
		return
	}
	err = nil
	return
}

func getCommonPrefix(remotePath string) (commonPrefix string) {
	remotePath = strings.TrimSuffix(remotePath, "/")
	pSlice := strings.Split(remotePath, "/")
	if len(pSlice) < 2 {
		return
	}
	/*
		eg: remotePath = "/", return value = ""
		remotePath = "/xyz", return value = ""
		remotePath = "/xyz/abc", return value = "abc"
		remotePath = "/xyz/abc/def", return value = "abc/def"
	*/
	return strings.Join(pSlice[2:], "/")
}

func isPathNoExistError(err error) bool {
	if err == nil {
		return false
	}

	switch err := err.(type) {
	case *zerror.Error:
		if err.Code == pathDoesNotExist {
			return true
		}
	}

	return false
}

func isConsensusFailedError(err error) bool {
	if err == nil {
		return false
	}

	switch err := err.(type) {
	case *zerror.Error:
		if err.Code == consensusFailed {
			return true
		}
	}
	return false
}

func isSameRootError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "previous allocation root are same")
}
