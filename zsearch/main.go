package main

import (
	"log"
	"net/http"
	dhandler "zsearch/delete/handler"
	dmodel "zsearch/delete/model"
	ihandler "zsearch/indexer/handler"
	"zsearch/indexer/model"
	"zsearch/search/handler"
	"zsearch/utility"

	"github.com/blevesearch/bleve/v2"
	"github.com/google/go-tika/tika"
)

func main() {

	client := tika.NewClient(nil, "http://tika:9998")

	index, err := utility.OpenOrCreateIndex("/vindex/files_index.bleve")
	if err != nil {
		log.Fatalln(err)
	}
	defer index.Close()

	isize, err := utility.SizeOfIndex("/vindex/files_index.bleve")
	if err != nil {
		log.Println("Error in calculating size", err)
	}
	log.Printf("Size of index is %d MB \n", isize/(1024*1024))

	jobChan := make(chan model.FileInfo, 10000)
	delChan := make(chan dmodel.DelReq, 10000)
	numWorkers := 20
	for i := 0; i < numWorkers; i++ {
		go StartIndexWorker(jobChan, index)
	}

	for i := 0; i < numWorkers; i++ {
		go StartDeleteWorker(delChan, index)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/index", ihandler.IndexHandler(jobChan, client))
	mux.HandleFunc("/search", handler.SearchHandler(index))
	mux.HandleFunc("/zindex", ihandler.PutIndexHandler(jobChan, client))
	mux.HandleFunc("/delete", dhandler.DeleteHandler(delChan))
	log.Println("Server is starting on port 3003")
	if err := http.ListenAndServe(":3003", mux); err != nil {
		log.Fatalln(err)
	}
}

func StartIndexWorker(jobChan <-chan model.FileInfo, index bleve.Index) {
	for job := range jobChan {
		log.Printf("indexing file %s", job.Path)
		cleanText := utility.CleanText(job.Content)
		job.Content = cleanText
		err := utility.IndexFiles(index, []model.FileInfo{job})
		if err != nil {
			log.Printf("Error indexing file %s: %+v", job.Path, err)
		} else {
			log.Printf("File indexed successfully: %s", job.Path)
		}
	}
}

func StartDeleteWorker(delChan <-chan dmodel.DelReq, index bleve.Index) {
	for dreq := range delChan {
		log.Printf("deleting file %s", dreq.Path)
		err := utility.DeleteFileFromIndex(index, dreq)
		if err != nil {
			log.Printf("Error deleting file %s: %+v", dreq.Path, err)
		} else {
			log.Printf("File deleted successfully: %s", dreq.Path)
		}
	}
}
