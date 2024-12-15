package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
)

const postURL = "http://localhost:2024/add"

func main() {
	var wg sync.WaitGroup

	for i := 0; i < 1500; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			caller(n)
		}(i)
	}

	wg.Wait()

}

func caller(n int) {
	c := http.DefaultClient
	name := strconv.Itoa(n)
	for i := 0; i < 1000; i++ {
		istr := strconv.Itoa(i)

		b := bytes.NewBufferString(name + "-" + istr)
		resp, err := c.Post(postURL, "application/text", b)
		if err != nil {
			panic(err)
		}
		if resp.StatusCode != 200 {
			panic("not OK: " + resp.Status)
		}
		defer resp.Body.Close()
		ans, err := io.ReadAll(resp.Body)
		if err != nil {
			panic(err)
		}
		if i%100 == 0 {
			fmt.Println("Added", string(ans))
		}
	}

}
