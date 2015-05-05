// Copyright 2015 Michael Johnson. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// package xmppresolv provides an HTTP API for resolving XMPP-related DNS records
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc64"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type server struct {
	Target   string `json:"target"`
	Port     uint16 `json:"port"`
	Priority uint16 `json:"priority"`
	Weight   uint16 `json:"weight"`
}

type serverList []*server

func (s serverList) Len() int {
	return len(s)
}

func (s serverList) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s serverList) Less(i, j int) bool {
	a, b := s[i], s[j]

	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}

	if a.Weight != b.Weight {
		return a.Weight < b.Weight
	}

	if a.Target != b.Target {
		return a.Target < b.Target
	}

	if a.Port != b.Port {
		return a.Port < b.Port
	}

	return false
}

type alternative struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type alternativeList []*alternative

func (s alternativeList) Len() int {
	return len(s)
}

func (s alternativeList) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s alternativeList) Less(i, j int) bool {
	a, b := s[i], s[j]

	if a.Name != b.Name {
		return a.Name < b.Name
	}

	if a.Value != b.Value {
		return a.Value < b.Value
	}

	return false
}

type response struct {
	Version string `json:"apiVersion"`

	Data *struct {
		Servers      serverList      `json:"servers"`
		Alternatives alternativeList `json:"alternatives"`
	} `json:"data,omitempty"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var crcTable = crc64.MakeTable(crc64.ISO)

func mustJSONEncode(data interface{}) string {
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}

	return string(encoded)
}

var (
	internalServerError = mustJSONEncode(&response{
		Version: "1.0",

		Error: &struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{
			Code:    500,
			Message: "An internal server error has occured.",
		},
	})
	notFoundError = mustJSONEncode(&response{
		Version: "1.0",

		Error: &struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{
			Code:    404,
			Message: "The given domain name does not contain any relevant records.",
		},
	})
)

// We duplicate the http.Error function because we don't want it to set
// Content-Type
func httpError(w http.ResponseWriter, error string, code int) {
	w.WriteHeader(code)
	fmt.Fprintln(w, error)
}

func serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, fmt.Sprintf("This resource does not accept %s requests.", r.Method), http.StatusMethodNotAllowed)
		return
	}

	domain := r.URL.Path[1:]

	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=900")
	h.Set("Access-Control-Allow-Origin", "*")

	srvFound := true
	_, srv, err := net.LookupSRV("xmpp-client", "tcp", domain)
	if err != nil {
		if !strings.HasSuffix(err.Error(), "DNS name does not exist.") {
			log.Printf("Error resolving SRV records for %q: %v", domain, err)
			httpError(w, internalServerError, http.StatusInternalServerError)
			return
		}

		srvFound = false
	}

	txtFound := true
	txt, err := net.LookupTXT("_xmppconnect." + domain)
	if err != nil {
		if !strings.HasSuffix(err.Error(), "DNS name does not exist.") {
			log.Printf("Error resolving TXT records for %q: %v", domain, err)
			httpError(w, internalServerError, http.StatusInternalServerError)
			return
		}

		txtFound = false
	}

	if !txtFound && !srvFound {
		httpError(w, notFoundError, http.StatusNotFound)
		return
	}

	res := &response{
		Version: "1.0",

		Data: &struct {
			Servers      serverList      `json:"servers"`
			Alternatives alternativeList `json:"alternatives"`
		}{
			Servers:      make([]*server, len(srv)),
			Alternatives: make([]*alternative, len(txt)),
		},
	}

	for i, service := range srv {
		res.Data.Servers[i] = &server{
			Target:   service.Target,
			Port:     service.Port,
			Priority: service.Priority,
			Weight:   service.Weight,
		}
	}

	for i, rec := range txt {
		split := strings.SplitN(rec, "=", 2)

		name := split[0]
		if !strings.HasPrefix(strings.ToLower(name), "_xmpp-client-") {
			continue
		}

		name = name[13:]

		res.Data.Alternatives[i] = &alternative{
			Name:  name,
			Value: split[1],
		}
	}

	if len(res.Data.Servers) == 0 && len(res.Data.Alternatives) == 0 {
		httpError(w, notFoundError, http.StatusNotFound)
		return
	}

	sort.Sort(res.Data.Servers)
	sort.Sort(res.Data.Alternatives)

	encoded, err := json.Marshal(res)
	if err != nil {
		log.Fatalf("Error marshalling JSON for %q: %v", domain, err)
	}

	hash := crc64.Checksum(encoded, crcTable)

	h.Set("ETag", "\""+strconv.FormatUint(hash, 16)+"\"")

	content := bytes.NewReader(encoded)
	http.ServeContent(w, r, domain, time.Time{}, content)
}

func main() {
	log.SetFlags(log.Lshortfile)

	http.HandleFunc("/", serve)

	log.Fatal(http.ListenAndServe("127.0.0.1:8080", nil))
}
