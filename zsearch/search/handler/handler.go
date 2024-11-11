package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"zsearch/utility"

	"github.com/blevesearch/bleve/v2"
	bq "github.com/blevesearch/bleve/v2/search/query"
)

func SearchHandler(index bleve.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		query := r.URL.Query().Get("query")
		if query == "" {
			http.Error(w, "Query parameter is required", http.StatusBadRequest)
			return
		}
		cquery := utility.CleanText(query)
		if cquery == "" {
			http.Error(w, "please use valid query", http.StatusBadRequest)
			return
		}
		cquery = strings.TrimSpace(cquery)
		log.Println("clean query", cquery)
		words := strings.Fields(cquery)
		var queries []bq.Query
		for _, word := range words {
			word = "*" + word + "*"
			queries = append(queries, bleve.NewWildcardQuery(word))
		}
		matchQuery := bleve.NewConjunctionQuery(queries...) // Or use NewDisjunctionQuery for "OR"
		searchRequest := bleve.NewSearchRequest(matchQuery)
		searchResult, err := index.Search(searchRequest)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		totalSearchTime := float64(time.Since(startTime).Milliseconds())
		log.Printf("Time taken for search %s is %f ms\n", query, totalSearchTime)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(searchResult)
	}
}
