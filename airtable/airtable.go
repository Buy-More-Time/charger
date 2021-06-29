package airtable

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var baseURL = "https://api.airtable.com/v0"

type Client struct {
	client http.Client
	apiKey string
	baseID string
	url    *url.URL
}

// ListRecordsOptions does not represent a 1:1 representation of what filter options
// are available. We have only listed those options which are relevant to our situation
type ListRecordsOptions struct {
	TableName       string
	Fields          []string
	FilterByFormula string
	PageSize        int
	Offset          string
}

type PartialUpdateOptions struct {
	TableName string
}

// Record represents a single airtable record. The record class is fairly simple, with Fields
// being the most complex set of the return
type Record struct {
	ID          string                 `json:"id"`
	CreatedTime time.Time              `json:"-"`
	Fields      map[string]interface{} `json:"fields"`
}

func NewAirtableClient(apiKey string, baseID string) (Client, error) {
	u, err := url.Parse(fmt.Sprintf("%s/%s", baseURL, baseID))
	if err != nil {
		return Client{}, err
	}

	return Client{
		apiKey: apiKey,
		baseID: baseID,
		url:    u,
	}, nil
}

type ListResponse struct {
	Records []Record `json:"records"`
	Offset  string   `json:"offset"`
}

type ListRequest struct {
	Records []Record `json:"records"`
}

func (c *Client) ListFromTable(options ListRecordsOptions) (list ListResponse, err error) {
	if options.TableName == "" {
		return list, errors.New("must provide a table name")
	}

	uri, err := url.Parse(fmt.Sprintf("%s/%s", c.url.String(), options.TableName))
	if err != nil {
		return list, err
	}

	query := uri.Query()

	// see documentation for formatting of options as query parameters
	for _, field := range options.Fields {
		query.Add("fields[]", field)
	}

	if options.PageSize > 0 {
		query.Add("pageSize", strconv.Itoa(options.PageSize))
	}

	if options.FilterByFormula != "" {
		query.Add("filterByFormula", options.FilterByFormula)
	}

	if options.Offset != "" {
		query.Add("offset", options.Offset)
	}

	uri.RawQuery = query.Encode()
	err = c.get(uri, &list)

	return list, err
}

func (c *Client) PartialUpdate(options PartialUpdateOptions, records ...Record) error {
	if len(records) > 10 {
		return errors.New("cannot update more than 10 records at once") // dumbest threshold ever for an app like airtable
	}

	uri, err := url.Parse(fmt.Sprintf("%s/%s", c.url.String(), options.TableName))
	if err != nil {
		return err
	}

	var request ListRequest
	request.Records = records

	return c.patch(uri, request, nil)
}

func (c *Client) get(url *url.URL, out interface{}) error {
	req, _ := http.NewRequest("GET", url.String(), nil)
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode < 199 || resp.StatusCode > 299 {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return errors.New(string(bodyBytes))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) patch(url *url.URL, in interface{}, out interface{}) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}

	req, _ := http.NewRequest("PATCH", url.String(), bytes.NewBuffer(payload))
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Add("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode < 199 || resp.StatusCode > 299 {
		return errors.New("api returned non-200 response")
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}

	return nil
}
