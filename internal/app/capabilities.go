package app

// Capability names one operation a storage backend implements. Backends
// declare the set they support when they register (backend.go); the service
// rejects calls for capabilities a backend does not declare, and the UI hides
// the entry points that need them.
//
// A capability says what the backend implements, not what the current account
// may do. AccessDenied from the service is still an error: a 403 never turns
// into "unsupported".
type Capability string

const (
	// Buckets.
	CapBucketList   Capability = "bucket.list"
	CapBucketCreate Capability = "bucket.create"
	CapBucketDelete Capability = "bucket.delete"
	CapBucketEmpty  Capability = "bucket.empty"
	CapBucketConfig Capability = "bucket.config"

	// Objects.
	CapObjectList         Capability = "object.list"
	CapObjectRead         Capability = "object.read"
	CapObjectUpload       Capability = "object.upload"
	CapObjectDownload     Capability = "object.download"
	CapObjectCopy         Capability = "object.copy"
	CapObjectMove         Capability = "object.move"
	CapObjectRename       Capability = "object.rename"
	CapObjectDelete       Capability = "object.delete"
	CapObjectFolderCreate Capability = "object.folder.create"
	CapObjectProperties   Capability = "object.properties"
	CapObjectTags         Capability = "object.tags"
	CapObjectACL          Capability = "object.acl"
	CapObjectStorageClass Capability = "object.storage-class"
	CapObjectEncryption   Capability = "object.encryption"
	CapObjectPresign      Capability = "object.presign"
	CapObjectVersions     Capability = "object.versions"

	// Cross-cutting services.
	CapMultipart  Capability = "multipart"
	CapSync       Capability = "sync"
	CapCloudFront Capability = "cloudfront"

	// MCSManager file manager.
	CapMCSInstanceList Capability = "mcs.instance.list"
	CapMCSFileList     Capability = "mcs.file.list"
	CapMCSFileDownload Capability = "mcs.file.download"
	CapMCSFileUpload   Capability = "mcs.file.upload"
	CapMCSFileDelete   Capability = "mcs.file.delete"
	CapMCSFileMkdir    Capability = "mcs.file.mkdir"
	CapMCSFileCopy     Capability = "mcs.file.copy"
	CapMCSFileMove     Capability = "mcs.file.move"
	CapMCSFileChmod    Capability = "mcs.file.chmod"
	CapMCSFileArchive  Capability = "mcs.file.archive"
	CapMCSFileText     Capability = "mcs.file.text"
)

// CapabilitySet is the set of capabilities a backend declares. A nil set
// declares nothing.
type CapabilitySet map[Capability]bool

// NewCapabilitySet returns a set holding cs.
func NewCapabilitySet(cs ...Capability) CapabilitySet {
	out := make(CapabilitySet, len(cs))
	for _, c := range cs {
		out[c] = true
	}
	return out
}

// Has reports whether c is in the set. It is safe on a nil set.
func (s CapabilitySet) Has(c Capability) bool { return s[c] }

// With returns a copy of s plus cs.
func (s CapabilitySet) With(cs ...Capability) CapabilitySet {
	out := make(CapabilitySet, len(s)+len(cs))
	for c := range s {
		out[c] = true
	}
	for _, c := range cs {
		out[c] = true
	}
	return out
}

// Clone returns an independent copy.
func (s CapabilitySet) Clone() CapabilitySet {
	if s == nil {
		return nil
	}
	out := make(CapabilitySet, len(s))
	for c := range s {
		out[c] = true
	}
	return out
}
