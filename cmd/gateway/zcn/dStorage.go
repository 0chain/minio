package zcn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	zerror "github.com/0chain/errors"
	"github.com/0chain/gosdk/zboxcore/sdk"
	"github.com/minio/minio/internal/logger"
	"github.com/mitchellh/go-homedir"
)

var tempdir string

const (
	pageLimit = 100
	dirType   = "d"
	fileType  = "f"

	defaultChunkSize = 64 * 1024
	fiveHunderedKB   = 500 * 1024
	oneMB            = 1024 * 1024
	tenMB            = 10 * oneMB
	hundredMB        = 10 * tenMB
	oneGB            = 1024 * oneMB

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
	commonPrefix := getCommonPrefix(remotePath)
	offsetPath := filepath.Join(remotePath, marker)
	for {
		oResult, err := getRegularRefs(alloc, remotePath, offsetPath, fileType, pageLimit)
		if err != nil {
			return nil, true, "", nil, err
		}
		if len(oResult.Refs) == 0 {
			break
		}

		for i := 0; i < len(oResult.Refs); i++ {
			ref := oResult.Refs[i]
			trimmedPath := strings.TrimPrefix(ref.Path, remotePath+"/")
			if isDelimited {
				if ref.Type == dirType {
					dirPrefix := filepath.Join(commonPrefix, trimmedPath) + "/"
					prefixes = append(prefixes, dirPrefix)
					continue
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
	if isTruncated {
		marker = strings.TrimPrefix(markedPath, remotePath+"/")
	} else {
		marker = ""
	}

	return refs, isTruncated, marker, prefixes, nil
}

func getRegularRefs(alloc *sdk.Allocation, remotePath, offsetPath, fileType string, pageLimit int) (oResult *sdk.ObjectTreeResult, err error) {
	level := len(strings.Split(strings.TrimSuffix(remotePath, "/"), "/")) + 1
	oResult, err = alloc.GetRefs(remotePath, offsetPath, "", "", fileType, "regular", level, pageLimit)
	return
}

func getSingleRegularRef(alloc *sdk.Allocation, remotePath string) (*sdk.ORef, error) {
	level := len(strings.Split(strings.TrimSuffix(remotePath, "/"), "/"))
	oREsult, err := alloc.GetRefs(remotePath, "", "", "", "", "regular", level, 1)
	if err != nil {
		logger.Error("error with GetRefs", err.Error())
		if isConsensusFailedError(err) {
			time.Sleep(retryWaitTime)
			oREsult, err = alloc.GetRefs(remotePath, "", "", "", "", "regular", level, 1)
			if err != nil {
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

func getFileReader(ctx context.Context, alloc *sdk.Allocation, remotePath string, fileSize uint64) (*os.File, string, error) {
	localFilePath := filepath.Join(tempdir, remotePath)
	os.Remove(localFilePath)

	cb := statusCB{
		doneCh: make(chan struct{}, 1),
		errCh:  make(chan error, 1),
	}

	var ctxCncl context.CancelFunc
	ctx, ctxCncl = context.WithTimeout(ctx, getTimeOut(fileSize))
	defer ctxCncl()

	err := alloc.DownloadFile(localFilePath, remotePath, false, &cb)
	if err != nil {
		return nil, "", err
	}

	select {
	case <-cb.doneCh:
	case err := <-cb.errCh:
		return nil, "", err
	case <-ctx.Done():
		return nil, "", errors.New("exceeded timeout")
	}

	r, err := os.Open(localFilePath)
	if err != nil {
		return nil, "", err
	}

	return r, localFilePath, nil
}

func putFile(ctx context.Context, alloc *sdk.Allocation, remotePath, contentType string, r io.Reader, size int64, isUpdate, shouldEncrypt bool) (err error) {
	logger.Info("started PutFile")
	cb := &statusCB{
		doneCh: make(chan struct{}, 1),
		errCh:  make(chan error, 1),
	}

	_, fileName := filepath.Split(remotePath)
	fileMeta := sdk.FileMeta{
		Path:       "",
		RemotePath: remotePath,
		ActualSize: size,
		MimeType:   contentType,
		RemoteName: fileName,
	}

	workDir, err := homedir.Dir()
	if err != nil {
		logger.Error(err.Error())
		return err
	}

	logger.Info("creating chunked upload")
	chunkUpload, err := sdk.CreateChunkedUpload(workDir, alloc, fileMeta, newMinioReader(r), isUpdate, false,
		sdk.WithStatusCallback(cb),
	)

	if err != nil {
		logger.Info("error from PutFile")
		logger.Error(err.Error())
		return
	}

	err = chunkUpload.Start()
	if err != nil {
		logger.Info("error from PutFile")
		logger.Error(err.Error())
		return
	}

	select {
	case <-cb.doneCh:
	case err = <-cb.errCh:
	}

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
