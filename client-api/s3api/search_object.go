package s3api

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
)

func searchObject(query string) ([]byte, error) {
	u, _ := url.Parse("http://zsearch:3003/search")
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()
	log.Println("search url", u.String())
	resp, err := http.Get(u.String())
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return body, nil
}
