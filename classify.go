package main

import (
	"net/http"
	"strings"
)

// Op names — granular audit labels. Class is the pricing tier the controlplane
// charges flat per request: A = mutating/listing, B = read/head, free = delete/abort.
const (
	opPutObject               = "put_object"
	opGetObject               = "get_object"
	opHeadObject              = "head_object"
	opDeleteObject            = "delete_object"
	opDeleteObjects           = "delete_objects"
	opListObjects             = "list_objects"
	opHeadBucket              = "head_bucket"
	opGetBucketLocation       = "get_bucket_location"
	opCreateMultipartUpload   = "create_multipart_upload"
	opUploadPart              = "upload_part"
	opCompleteMultipartUpload = "complete_multipart_upload"
	opAbortMultipartUpload    = "abort_multipart_upload"
	opListMultipartUploads    = "list_multipart_uploads"
	opListParts               = "list_parts"
	opUnknown                 = "unknown"
)

const (
	classA    = "a"
	classB    = "b"
	classFree = "free"
)

type opMeta struct {
	Name  string
	Class string
}

// classify maps an inbound S3-style request to one of our op labels.
// Unknown shapes fall through as opUnknown with no class so the controlplane
// pricing registry leaves them unbilled but still visible for debugging.
func classify(r *http.Request) opMeta {
	q := r.URL.Query()
	keyed := keyedPath(r.URL.Path)

	switch r.Method {
	case http.MethodGet:
		switch {
		case !keyed && q.Has("uploads"):
			return opMeta{opListMultipartUploads, classA}
		case !keyed && q.Has("location"):
			return opMeta{opGetBucketLocation, classFree}
		case !keyed:
			return opMeta{opListObjects, classA}
		case q.Has("uploadId"):
			return opMeta{opListParts, classA}
		default:
			return opMeta{opGetObject, classB}
		}
	case http.MethodHead:
		if !keyed {
			return opMeta{opHeadBucket, classFree}
		}
		return opMeta{opHeadObject, classB}
	case http.MethodPut:
		if q.Has("partNumber") && q.Has("uploadId") {
			return opMeta{opUploadPart, classA}
		}
		return opMeta{opPutObject, classA}
	case http.MethodPost:
		switch {
		case q.Has("uploads"):
			return opMeta{opCreateMultipartUpload, classA}
		case q.Has("uploadId"):
			return opMeta{opCompleteMultipartUpload, classA}
		case !keyed && q.Has("delete"):
			return opMeta{opDeleteObjects, classFree}
		}
	case http.MethodDelete:
		if q.Has("uploadId") {
			return opMeta{opAbortMultipartUpload, classFree}
		}
		return opMeta{opDeleteObject, classFree}
	}
	return opMeta{opUnknown, ""}
}

// keyedPath reports whether the URL path addresses an object (/bucket/key...)
// vs a bucket (/bucket, /bucket/, or /). A bare bucket with a trailing slash
// is still bucket-level — the sidecar routes /{bucket} and /{bucket}/ to the
// same handlers.
func keyedPath(p string) bool {
	if p == "" || p == "/" {
		return false
	}
	rest := strings.TrimSuffix(p[1:], "/")
	return strings.IndexByte(rest, '/') >= 0
}
