/*
 * Minio Cloud Storage, (C) 2016 Minio, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cmd

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/gorilla/mux"
	"github.com/gorilla/rpc/v2/json2"
	"github.com/minio/minio-go/pkg/policy"
	"github.com/minio/minio/browser"
)

// WebGenericArgs - empty struct for calls that don't accept arguments
// for ex. ServerInfo, GenerateAuth
type WebGenericArgs struct{}

// WebGenericRep - reply structure for calls for which reply is success/failure
// for ex. RemoveObject MakeBucket
type WebGenericRep struct {
	UIVersion string `json:"uiVersion"`
}

// ServerInfoRep - server info reply.
type ServerInfoRep struct {
	MinioVersion  string
	MinioMemory   string
	MinioPlatform string
	MinioRuntime  string
	MinioEnvVars  []string
	UIVersion     string `json:"uiVersion"`
}

// ServerInfo - get server info.
func (web *webAPIHandlers) ServerInfo(r *http.Request, args *WebGenericArgs, reply *ServerInfoRep) error {
	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	memstats := &runtime.MemStats{}
	runtime.ReadMemStats(memstats)
	mem := fmt.Sprintf("Used: %s | Allocated: %s | Used-Heap: %s | Allocated-Heap: %s",
		humanize.Bytes(memstats.Alloc),
		humanize.Bytes(memstats.TotalAlloc),
		humanize.Bytes(memstats.HeapAlloc),
		humanize.Bytes(memstats.HeapSys))
	platform := fmt.Sprintf("Host: %s | OS: %s | Arch: %s",
		host,
		runtime.GOOS,
		runtime.GOARCH)
	goruntime := fmt.Sprintf("Version: %s | CPUs: %s", runtime.Version(), strconv.Itoa(runtime.NumCPU()))

	reply.MinioEnvVars = os.Environ()
	reply.MinioVersion = Version
	reply.MinioMemory = mem
	reply.MinioPlatform = platform
	reply.MinioRuntime = goruntime
	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// StorageInfoRep - contains storage usage statistics.
type StorageInfoRep struct {
	StorageInfo StorageInfo `json:"storageInfo"`
	UIVersion   string      `json:"uiVersion"`
}

// StorageInfo - web call to gather storage usage statistics.
func (web *webAPIHandlers) StorageInfo(r *http.Request, args *AuthRPCArgs, reply *StorageInfoRep) error {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		return toJSONError(errServerNotInitialized)
	}
	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}
	reply.StorageInfo = objectAPI.StorageInfo()
	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// MakeBucketArgs - make bucket args.
type MakeBucketArgs struct {
	BucketName string `json:"bucketName"`
}

// MakeBucket - make a bucket.
func (web *webAPIHandlers) MakeBucket(r *http.Request, args *MakeBucketArgs, reply *WebGenericRep) error {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		return toJSONError(errServerNotInitialized)
	}
	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}
	bucketLock := globalNSMutex.NewNSLock(args.BucketName, "")
	bucketLock.Lock()
	defer bucketLock.Unlock()
	if err := objectAPI.MakeBucket(args.BucketName); err != nil {
		return toJSONError(err, args.BucketName)
	}
	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// ListBucketsRep - list buckets response
type ListBucketsRep struct {
	Buckets   []WebBucketInfo `json:"buckets"`
	UIVersion string          `json:"uiVersion"`
}

// WebBucketInfo container for list buckets metadata.
type WebBucketInfo struct {
	// The name of the bucket.
	Name string `json:"name"`
	// Date the bucket was created.
	CreationDate time.Time `json:"creationDate"`
}

// ListBuckets - list buckets api.
func (web *webAPIHandlers) ListBuckets(r *http.Request, args *WebGenericArgs, reply *ListBucketsRep) error {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		return toJSONError(errServerNotInitialized)
	}
	authErr := webRequestAuthenticate(r)
	if authErr != nil {
		return toJSONError(authErr)
	}
	buckets, err := objectAPI.ListBuckets()
	if err != nil {
		return toJSONError(err)
	}
	for _, bucket := range buckets {
		if bucket.Name == path.Base(reservedBucket) {
			continue
		}

		reply.Buckets = append(reply.Buckets, WebBucketInfo{
			Name:         bucket.Name,
			CreationDate: bucket.Created,
		})
	}
	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// ListObjectsArgs - list object args.
type ListObjectsArgs struct {
	BucketName string `json:"bucketName"`
	Prefix     string `json:"prefix"`
	Marker     string `json:"marker"`
}

// ListObjectsRep - list objects response.
type ListObjectsRep struct {
	Objects     []WebObjectInfo `json:"objects"`
	NextMarker  string          `json:"nextmarker"`
	IsTruncated bool            `json:"istruncated"`
	Writable    bool            `json:"writable"` // Used by client to show "upload file" button.
	UIVersion   string          `json:"uiVersion"`
}

// WebObjectInfo container for list objects metadata.
type WebObjectInfo struct {
	// Name of the object
	Key string `json:"name"`
	// Date and time the object was last modified.
	LastModified time.Time `json:"lastModified"`
	// Size in bytes of the object.
	Size int64 `json:"size"`
	// ContentType is mime type of the object.
	ContentType string `json:"contentType"`
}

// ListObjects - list objects api.
func (web *webAPIHandlers) ListObjects(r *http.Request, args *ListObjectsArgs, reply *ListObjectsRep) error {
	reply.UIVersion = miniobrowser.UIVersion
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		return toJSONError(errServerNotInitialized)
	}
	prefix := args.Prefix + "test" // To test if GetObject/PutObject with the specified prefix is allowed.
	readable := isBucketActionAllowed("s3:GetObject", args.BucketName, prefix)
	writable := isBucketActionAllowed("s3:PutObject", args.BucketName, prefix)
	authErr := webRequestAuthenticate(r)
	switch {
	case authErr == errAuthentication:
		return toJSONError(authErr)
	case authErr == nil:
		break
	case readable && writable:
		reply.Writable = true
		break
	case readable:
		break
	case writable:
		reply.Writable = true
		return nil
	default:
		return errAuthentication
	}
	lo, err := objectAPI.ListObjects(args.BucketName, args.Prefix, args.Marker, slashSeparator, 1000)
	if err != nil {
		return &json2.Error{Message: err.Error()}
	}
	reply.NextMarker = lo.NextMarker
	reply.IsTruncated = lo.IsTruncated
	for _, obj := range lo.Objects {
		reply.Objects = append(reply.Objects, WebObjectInfo{
			Key:          obj.Name,
			LastModified: obj.ModTime,
			Size:         obj.Size,
			ContentType:  obj.ContentType,
		})
	}
	for _, prefix := range lo.Prefixes {
		reply.Objects = append(reply.Objects, WebObjectInfo{
			Key: prefix,
		})
	}

	return nil
}

// RemoveObjectArgs - args to remove an object
type RemoveObjectArgs struct {
	TargetHost string `json:"targetHost"`
	BucketName string `json:"bucketName"`
	ObjectName string `json:"objectName"`
}

// RemoveObject - removes an object.
func (web *webAPIHandlers) RemoveObject(r *http.Request, args *RemoveObjectArgs, reply *WebGenericRep) error {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		return toJSONError(errServerNotInitialized)
	}
	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}

	objectLock := globalNSMutex.NewNSLock(args.BucketName, args.ObjectName)
	objectLock.Lock()
	defer objectLock.Unlock()

	if err := objectAPI.DeleteObject(args.BucketName, args.ObjectName); err != nil {
		if isErrObjectNotFound(err) {
			// Ignore object not found error.
			reply.UIVersion = miniobrowser.UIVersion
			return nil
		}
		return toJSONError(err, args.BucketName, args.ObjectName)
	}

	// Notify object deleted event.
	eventNotify(eventData{
		Type:   ObjectRemovedDelete,
		Bucket: args.BucketName,
		ObjInfo: ObjectInfo{
			Name: args.ObjectName,
		},
		ReqParams: map[string]string{
			"sourceIPAddress": r.RemoteAddr,
		},
	})

	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// LoginArgs - login arguments.
type LoginArgs struct {
	Username string `json:"username" form:"username"`
	Password string `json:"password" form:"password"`
}

// LoginRep - login reply.
type LoginRep struct {
	Token     string `json:"token"`
	UIVersion string `json:"uiVersion"`
}

// Login - user login handler.
func (web *webAPIHandlers) Login(r *http.Request, args *LoginArgs, reply *LoginRep) error {
	token, err := authenticateWeb(args.Username, args.Password)
	if err != nil {
		// Make sure to log errors related to browser login,
		// for security and auditing reasons.
		errorIf(err, "Unable to login request from %s", r.RemoteAddr)
		return toJSONError(err)
	}

	reply.Token = token
	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// GenerateAuthReply - reply for GenerateAuth
type GenerateAuthReply struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	UIVersion string `json:"uiVersion"`
}

func (web webAPIHandlers) GenerateAuth(r *http.Request, args *WebGenericArgs, reply *GenerateAuthReply) error {
	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}
	cred := newCredential()
	reply.AccessKey = cred.AccessKey
	reply.SecretKey = cred.SecretKey
	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// SetAuthArgs - argument for SetAuth
type SetAuthArgs struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// SetAuthReply - reply for SetAuth
type SetAuthReply struct {
	Token       string            `json:"token"`
	UIVersion   string            `json:"uiVersion"`
	PeerErrMsgs map[string]string `json:"peerErrMsgs"`
}

// SetAuth - Set accessKey and secretKey credentials.
func (web *webAPIHandlers) SetAuth(r *http.Request, args *SetAuthArgs, reply *SetAuthReply) error {
	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}

	// If creds are set through ENV disallow changing credentials.
	if globalIsEnvCreds {
		return toJSONError(errChangeCredNotAllowed)
	}

	// As we already validated the authentication, we save given access/secret keys.
	if err := validateAuthKeys(args.AccessKey, args.SecretKey); err != nil {
		return toJSONError(err)
	}

	creds := credential{
		AccessKey: args.AccessKey,
		SecretKey: args.SecretKey,
	}

	// Notify all other Minio peers to update credentials
	errsMap := updateCredsOnPeers(creds)

	// Update local credentials
	serverConfig.SetCredential(creds)

	// Persist updated credentials.
	if err := serverConfig.Save(); err != nil {
		errsMap[globalMinioAddr] = err
	}

	// Log all the peer related error messages, and populate the
	// PeerErrMsgs map.
	reply.PeerErrMsgs = make(map[string]string)
	for svr, errVal := range errsMap {
		tErr := fmt.Errorf("Unable to change credentials on %s: %v", svr, errVal)
		errorIf(tErr, "Credentials change could not be propagated successfully!")
		reply.PeerErrMsgs[svr] = errVal.Error()
	}

	// If we were unable to update locally, we return an error to the user/browser.
	if errsMap[globalMinioAddr] != nil {
		// Since the error message may be very long to display
		// on the browser, we tell the user to check the
		// server logs.
		return toJSONError(errors.New("unexpected error(s) occurred - please check minio server logs"))
	}

	// As we have updated access/secret key, generate new auth token.
	token, err := authenticateWeb(creds.AccessKey, creds.SecretKey)
	if err != nil {
		// Did we have peer errors?
		if len(errsMap) > 0 {
			err = fmt.Errorf(
				"we gave up due to: '%s', but there were more errors. Please check minio server logs",
				err.Error(),
			)
		}

		return toJSONError(err)
	}

	reply.Token = token
	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// GetAuthReply - Reply current credentials.
type GetAuthReply struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	UIVersion string `json:"uiVersion"`
}

// GetAuth - return accessKey and secretKey credentials.
func (web *webAPIHandlers) GetAuth(r *http.Request, args *WebGenericArgs, reply *GetAuthReply) error {
	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}
	creds := serverConfig.GetCredential()
	reply.AccessKey = creds.AccessKey
	reply.SecretKey = creds.SecretKey
	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// Upload - file upload handler.
func (web *webAPIHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		writeWebErrorResponse(w, errServerNotInitialized)
		return
	}

	vars := mux.Vars(r)
	bucket := vars["bucket"]
	object := vars["object"]

	authErr := webRequestAuthenticate(r)
	if authErr == errAuthentication {
		writeWebErrorResponse(w, errAuthentication)
		return
	}
	if authErr != nil && !isBucketActionAllowed("s3:PutObject", bucket, object) {
		writeWebErrorResponse(w, errAuthentication)
		return
	}

	// Require Content-Length to be set in the request
	size := r.ContentLength
	if size < 0 {
		writeWebErrorResponse(w, errSizeUnspecified)
		return
	}

	// Extract incoming metadata if any.
	metadata := extractMetadataFromHeader(r.Header)

	// Lock the object.
	objectLock := globalNSMutex.NewNSLock(bucket, object)
	objectLock.Lock()
	defer objectLock.Unlock()

	sha256sum := ""
	objInfo, err := objectAPI.PutObject(bucket, object, size, r.Body, metadata, sha256sum)
	if err != nil {
		writeWebErrorResponse(w, err)
		return
	}

	// Notify object created event.
	eventNotify(eventData{
		Type:    ObjectCreatedPut,
		Bucket:  bucket,
		ObjInfo: objInfo,
		ReqParams: map[string]string{
			"sourceIPAddress": r.RemoteAddr,
		},
	})
}

// Download - file download handler.
func (web *webAPIHandlers) Download(w http.ResponseWriter, r *http.Request) {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		writeWebErrorResponse(w, errServerNotInitialized)
		return
	}

	vars := mux.Vars(r)
	bucket := vars["bucket"]
	object := vars["object"]
	token := r.URL.Query().Get("token")

	if !isAuthTokenValid(token) && !isBucketActionAllowed("s3:GetObject", bucket, object) {
		writeWebErrorResponse(w, errAuthentication)
		return
	}

	// Add content disposition.
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", path.Base(object)))

	// Lock the object before reading.
	objectLock := globalNSMutex.NewNSLock(bucket, object)
	objectLock.RLock()
	defer objectLock.RUnlock()

	if err := objectAPI.GetObject(bucket, object, 0, -1, w); err != nil {
		/// No need to print error, response writer already written to.
		return
	}
}

// DownloadZipArgs - Argument for downloading a bunch of files as a zip file.
// JSON will look like:
// '{"bucketname":"testbucket","prefix":"john/pics/","objects":["hawaii/","maldives/","sanjose.jpg"]}'
type DownloadZipArgs struct {
	Objects    []string `json:"objects"`    // can be files or sub-directories
	Prefix     string   `json:"prefix"`     // current directory in the browser-ui
	BucketName string   `json:"bucketname"` // bucket name.
}

// Takes a list of objects and creates a zip file that sent as the response body.
func (web *webAPIHandlers) DownloadZip(w http.ResponseWriter, r *http.Request) {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		writeWebErrorResponse(w, errServerNotInitialized)
		return
	}

	token := r.URL.Query().Get("token")

	if !isAuthTokenValid(token) {
		writeWebErrorResponse(w, errAuthentication)
		return
	}
	var args DownloadZipArgs
	decodeErr := json.NewDecoder(r.Body).Decode(&args)
	if decodeErr != nil {
		writeWebErrorResponse(w, decodeErr)
		return
	}

	archive := zip.NewWriter(w)
	defer archive.Close()

	for _, object := range args.Objects {
		// Writes compressed object file to the response.
		zipit := func(objectName string) error {
			info, err := objectAPI.GetObjectInfo(args.BucketName, objectName)
			if err != nil {
				return err
			}
			header := &zip.FileHeader{
				Name:               strings.TrimPrefix(objectName, args.Prefix),
				Method:             zip.Deflate,
				UncompressedSize64: uint64(info.Size),
				UncompressedSize:   uint32(info.Size),
			}
			writer, err := archive.CreateHeader(header)
			if err != nil {
				writeWebErrorResponse(w, errUnexpected)
				return err
			}
			return objectAPI.GetObject(args.BucketName, objectName, 0, info.Size, writer)
		}

		if !strings.HasSuffix(object, "/") {
			// If not a directory, compress the file and write it to response.
			err := zipit(pathJoin(args.Prefix, object))
			if err != nil {
				return
			}
			continue
		}

		// For directories, list the contents recursively and write the objects as compressed
		// date to the response writer.
		marker := ""
		for {
			lo, err := objectAPI.ListObjects(args.BucketName, pathJoin(args.Prefix, object), marker, "", 1000)
			if err != nil {
				return
			}
			marker = lo.NextMarker
			for _, obj := range lo.Objects {
				err = zipit(obj.Name)
				if err != nil {
					return
				}
			}
			if !lo.IsTruncated {
				break
			}
		}
	}
}

// GetBucketPolicyArgs - get bucket policy args.
type GetBucketPolicyArgs struct {
	BucketName string `json:"bucketName"`
	Prefix     string `json:"prefix"`
}

// GetBucketPolicyRep - get bucket policy reply.
type GetBucketPolicyRep struct {
	UIVersion string              `json:"uiVersion"`
	Policy    policy.BucketPolicy `json:"policy"`
}

func readBucketAccessPolicy(objAPI ObjectLayer, bucketName string) (policy.BucketAccessPolicy, error) {
	bucketPolicyReader, err := readBucketPolicyJSON(bucketName, objAPI)
	if err != nil {
		if _, ok := err.(BucketPolicyNotFound); ok {
			return policy.BucketAccessPolicy{Version: "2012-10-17"}, nil
		}
		return policy.BucketAccessPolicy{}, err
	}

	bucketPolicyBuf, err := ioutil.ReadAll(bucketPolicyReader)
	if err != nil {
		return policy.BucketAccessPolicy{}, err
	}

	policyInfo := policy.BucketAccessPolicy{}
	err = json.Unmarshal(bucketPolicyBuf, &policyInfo)
	if err != nil {
		return policy.BucketAccessPolicy{}, err
	}

	return policyInfo, nil

}

// GetBucketPolicy - get bucket policy.
func (web *webAPIHandlers) GetBucketPolicy(r *http.Request, args *GetBucketPolicyArgs, reply *GetBucketPolicyRep) error {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		return toJSONError(errServerNotInitialized)
	}

	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}

	policyInfo, err := readBucketAccessPolicy(objectAPI, args.BucketName)
	if err != nil {
		return toJSONError(err, args.BucketName)
	}

	reply.UIVersion = miniobrowser.UIVersion
	reply.Policy = policy.GetPolicy(policyInfo.Statements, args.BucketName, args.Prefix)

	return nil
}

// ListAllBucketPoliciesArgs - get all bucket policies.
type ListAllBucketPoliciesArgs struct {
	BucketName string `json:"bucketName"`
}

// Collection of canned bucket policy at a given prefix.
type bucketAccessPolicy struct {
	Prefix string              `json:"prefix"`
	Policy policy.BucketPolicy `json:"policy"`
}

// ListAllBucketPoliciesRep - get all bucket policy reply.
type ListAllBucketPoliciesRep struct {
	UIVersion string               `json:"uiVersion"`
	Policies  []bucketAccessPolicy `json:"policies"`
}

// GetllBucketPolicy - get all bucket policy.
func (web *webAPIHandlers) ListAllBucketPolicies(r *http.Request, args *ListAllBucketPoliciesArgs, reply *ListAllBucketPoliciesRep) error {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		return toJSONError(errServerNotInitialized)
	}

	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}

	policyInfo, err := readBucketAccessPolicy(objectAPI, args.BucketName)
	if err != nil {
		return toJSONError(err, args.BucketName)
	}

	reply.UIVersion = miniobrowser.UIVersion
	for prefix, policy := range policy.GetPolicies(policyInfo.Statements, args.BucketName) {
		reply.Policies = append(reply.Policies, bucketAccessPolicy{
			Prefix: prefix,
			Policy: policy,
		})
	}
	return nil
}

// SetBucketPolicyArgs - set bucket policy args.
type SetBucketPolicyArgs struct {
	BucketName string `json:"bucketName"`
	Prefix     string `json:"prefix"`
	Policy     string `json:"policy"`
}

// SetBucketPolicy - set bucket policy.
func (web *webAPIHandlers) SetBucketPolicy(r *http.Request, args *SetBucketPolicyArgs, reply *WebGenericRep) error {
	objectAPI := web.ObjectAPI()
	if objectAPI == nil {
		return toJSONError(errServerNotInitialized)
	}

	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}

	bucketP := policy.BucketPolicy(args.Policy)
	if !bucketP.IsValidBucketPolicy() {
		return &json2.Error{
			Message: "Invalid policy type " + args.Policy,
		}
	}

	policyInfo, err := readBucketAccessPolicy(objectAPI, args.BucketName)
	if err != nil {
		return toJSONError(err, args.BucketName)
	}
	policyInfo.Statements = policy.SetPolicy(policyInfo.Statements, bucketP, args.BucketName, args.Prefix)
	if len(policyInfo.Statements) == 0 {
		err = persistAndNotifyBucketPolicyChange(args.BucketName, policyChange{true, nil}, objectAPI)
		if err != nil {
			return toJSONError(err, args.BucketName)
		}
		reply.UIVersion = miniobrowser.UIVersion
		return nil
	}
	data, err := json.Marshal(policyInfo)
	if err != nil {
		return toJSONError(err)
	}

	// Parse validate and save bucket policy.
	if s3Error := parseAndPersistBucketPolicy(args.BucketName, data, objectAPI); s3Error != ErrNone {
		apiErr := getAPIError(s3Error)
		var err error
		if apiErr.Code == "XMinioPolicyNesting" {
			err = PolicyNesting{}
		} else {
			err = errors.New(apiErr.Description)
		}
		return toJSONError(err, args.BucketName)
	}
	reply.UIVersion = miniobrowser.UIVersion
	return nil
}

// PresignedGetArgs - presigned-get API args.
type PresignedGetArgs struct {
	// Host header required for signed headers.
	HostName string `json:"host"`

	// Bucket name of the object to be presigned.
	BucketName string `json:"bucket"`

	// Object name to be presigned.
	ObjectName string `json:"object"`

	// Expiry in seconds.
	Expiry int64 `json:"expiry"`
}

// PresignedGetRep - presigned-get URL reply.
type PresignedGetRep struct {
	UIVersion string `json:"uiVersion"`
	// Presigned URL of the object.
	URL string `json:"url"`
}

// PresignedGET - returns presigned-Get url.
func (web *webAPIHandlers) PresignedGet(r *http.Request, args *PresignedGetArgs, reply *PresignedGetRep) error {
	if !isHTTPRequestValid(r) {
		return toJSONError(errAuthentication)
	}

	if args.BucketName == "" || args.ObjectName == "" {
		return &json2.Error{
			Message: "Bucket and Object are mandatory arguments.",
		}
	}
	reply.UIVersion = miniobrowser.UIVersion
	reply.URL = presignedGet(args.HostName, args.BucketName, args.ObjectName, args.Expiry)
	return nil
}

// Returns presigned url for GET method.
func presignedGet(host, bucket, object string, expiry int64) string {
	cred := serverConfig.GetCredential()
	region := serverConfig.GetRegion()

	accessKey := cred.AccessKey
	secretKey := cred.SecretKey

	date := time.Now().UTC()
	dateStr := date.Format(iso8601Format)
	credential := fmt.Sprintf("%s/%s", accessKey, getScope(date, region))

	var expiryStr = "604800" // Default set to be expire in 7days.
	if expiry < 604800 && expiry > 0 {
		expiryStr = strconv.FormatInt(expiry, 10)
	}
	query := strings.Join([]string{
		"X-Amz-Algorithm=" + signV4Algorithm,
		"X-Amz-Credential=" + strings.Replace(credential, "/", "%2F", -1),
		"X-Amz-Date=" + dateStr,
		"X-Amz-Expires=" + expiryStr,
		"X-Amz-SignedHeaders=host",
	}, "&")

	path := "/" + path.Join(bucket, object)

	// Headers are empty, since "host" is the only header required to be signed for Presigned URLs.
	var extractedSignedHeaders http.Header

	canonicalRequest := getCanonicalRequest(extractedSignedHeaders, unsignedPayload, query, path, "GET", host)
	stringToSign := getStringToSign(canonicalRequest, date, getScope(date, region))
	signingKey := getSigningKey(secretKey, date, region)
	signature := getSignature(signingKey, stringToSign)

	// Construct the final presigned URL.
	return host + path + "?" + query + "&" + "X-Amz-Signature=" + signature
}

// toJSONError converts regular errors into more user friendly
// and consumable error message for the browser UI.
func toJSONError(err error, params ...string) (jerr *json2.Error) {
	apiErr := toWebAPIError(err)
	jerr = &json2.Error{
		Message: apiErr.Description,
	}
	switch apiErr.Code {
	// Bucket name invalid with custom error message.
	case "InvalidBucketName":
		if len(params) > 0 {
			jerr = &json2.Error{
				Message: fmt.Sprintf("Bucket Name %s is invalid. Lowercase letters, period, numerals are the only allowed characters and should be minimum 3 characters in length.", params[0]),
			}
		}
	// Bucket not found custom error message.
	case "NoSuchBucket":
		if len(params) > 0 {
			jerr = &json2.Error{
				Message: fmt.Sprintf("The specified bucket %s does not exist.", params[0]),
			}
		}
	// Object not found custom error message.
	case "NoSuchKey":
		if len(params) > 1 {
			jerr = &json2.Error{
				Message: fmt.Sprintf("The specified key %s does not exist", params[1]),
			}
		}
		// Add more custom error messages here with more context.
	}
	return jerr
}

// toWebAPIError - convert into error into APIError.
func toWebAPIError(err error) APIError {
	err = errorCause(err)
	if err == errAuthentication {
		return APIError{
			Code:           "AccessDenied",
			HTTPStatusCode: http.StatusForbidden,
			Description:    err.Error(),
		}
	} else if err == errServerNotInitialized {
		return APIError{
			Code:           "XMinioServerNotInitialized",
			HTTPStatusCode: http.StatusServiceUnavailable,
			Description:    err.Error(),
		}
	} else if err == errInvalidAccessKeyLength {
		return APIError{
			Code:           "AccessDenied",
			HTTPStatusCode: http.StatusForbidden,
			Description:    err.Error(),
		}
	} else if err == errInvalidSecretKeyLength {
		return APIError{
			Code:           "AccessDenied",
			HTTPStatusCode: http.StatusForbidden,
			Description:    err.Error(),
		}
	} else if err == errInvalidAccessKeyID {
		return APIError{
			Code:           "AccessDenied",
			HTTPStatusCode: http.StatusForbidden,
			Description:    err.Error(),
		}
	} else if err == errSizeUnspecified {
		return APIError{
			Code:           "InvalidRequest",
			HTTPStatusCode: http.StatusBadRequest,
			Description:    err.Error(),
		}
	} else if err == errChangeCredNotAllowed {
		return APIError{
			Code:           "MethodNotAllowed",
			HTTPStatusCode: http.StatusMethodNotAllowed,
			Description:    err.Error(),
		}
	}
	// Convert error type to api error code.
	var apiErrCode APIErrorCode
	switch err.(type) {
	case StorageFull:
		apiErrCode = ErrStorageFull
	case BucketNotFound:
		apiErrCode = ErrNoSuchBucket
	case BucketExists:
		apiErrCode = ErrBucketAlreadyOwnedByYou
	case BucketNameInvalid:
		apiErrCode = ErrInvalidBucketName
	case BadDigest:
		apiErrCode = ErrBadDigest
	case IncompleteBody:
		apiErrCode = ErrIncompleteBody
	case ObjectExistsAsDirectory:
		apiErrCode = ErrObjectExistsAsDirectory
	case ObjectNotFound:
		apiErrCode = ErrNoSuchKey
	case ObjectNameInvalid:
		apiErrCode = ErrNoSuchKey
	case InsufficientWriteQuorum:
		apiErrCode = ErrWriteQuorum
	case InsufficientReadQuorum:
		apiErrCode = ErrReadQuorum
	case PolicyNesting:
		apiErrCode = ErrPolicyNesting
	default:
		// Log unexpected and unhandled errors.
		errorIf(err, errUnexpected.Error())
		apiErrCode = ErrInternalError
	}
	apiErr := getAPIError(apiErrCode)
	return apiErr
}

// writeWebErrorResponse - set HTTP status code and write error description to the body.
func writeWebErrorResponse(w http.ResponseWriter, err error) {
	apiErr := toWebAPIError(err)
	w.WriteHeader(apiErr.HTTPStatusCode)
	w.Write([]byte(apiErr.Description))
}
