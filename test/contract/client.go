package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/vmihailenco/msgpack/v5"
)

// Client 是契约测试用的 HTTP 客户端：请求体默认 msgpack，响应按 Content-Type 解码。
type Client struct {
	base string
	hc   *http.Client
}

func NewClient(base string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), hc: &http.Client{}}
}

// Do 发送请求；body 为 nil 时不携带请求体。返回值：HTTP 状态码与解码后的响应体。
func (c *Client) Do(method, path string, body any) (int, any, error) {
	var reader io.Reader
	if body != nil {
		data, err := msgpack.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/msgpack")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if len(raw) == 0 {
		return resp.StatusCode, nil, nil // 如 204 空响应
	}

	var out any
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "msgpack") {
		err = msgpack.Unmarshal(raw, &out)
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		err = dec.Decode(&out)
	}
	return resp.StatusCode, out, err
}
