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
	Name       string
	Class      string
	BytesAdded uint64
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
			return opMeta{opListMultipartUploads, classA, 0}
		case !keyed && q.Has("location"):
			return opMeta{opGetBucketLocation, classB, 0}
		case !keyed:
			return opMeta{opListObjects, classA, 0}
		case q.Has("uploadId"):
			return opMeta{opListParts, classA, 0}
		default:
			return opMeta{opGetObject, classB, 0}
		}
	case http.MethodHead:
		if !keyed {
			return opMeta{opHeadBucket, classB, 0}
		}
		return opMeta{opHeadObject, classB, 0}
	case http.MethodPut:
		if q.Has("partNumber") && q.Has("uploadId") {
			return opMeta{opUploadPart, classA, contentLength(r)}
		}
		return opMeta{opPutObject, classA, contentLength(r)}
	case http.MethodPost:
		switch {
		case q.Has("uploads"):
			return opMeta{opCreateMultipartUpload, classA, 0}
		case q.Has("uploadId"):
			// We already report bytes_added per upload_part, so don't double-count.
			return opMeta{opCompleteMultipartUpload, classA, 0}
		case !keyed && q.Has("delete"):
			// Batch DeleteObjects — bills as free like single-object DELETE.
			return opMeta{opDeleteObjects, classFree, 0}
		}
	case http.MethodDelete:
		if q.Has("uploadId") {
			return opMeta{opAbortMultipartUpload, classFree, 0}
		}
		return opMeta{opDeleteObject, classFree, 0}
	}
	return opMeta{opUnknown, "", 0}
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

func contentLength(r *http.Request) uint64 {
	if r.ContentLength <= 0 {
		return 0
	}
	return uint64(r.ContentLength)
}
