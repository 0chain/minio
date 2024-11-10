package handler

import (
	"net/http"
	"zsearch/delete/model"
)

func DeleteHandler(delChan chan<- model.DelReq) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.URL.Query().Get("bucketName")
		objName := r.URL.Query().Get("objName")
		delReq := model.DelReq{
			Path:     bucketName + "/" + objName,
			Filename: objName,
		}

		delChan <- delReq
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Files deleted successfully"))
	}
}
