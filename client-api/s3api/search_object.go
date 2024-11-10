package s3api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func searchObject(query string) (interface{}, error) {
	url := fmt.Sprintf("http://zsearch:3003/search?query=%s", query)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return result, nil

}
