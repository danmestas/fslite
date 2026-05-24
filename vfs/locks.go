package vfs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/net/webdav"
)

// natsLockSystem implements webdav.LockSystem with cross-agent coordination
// through a JetStream KV bucket. An exclusive lock acquired by one agent
// becomes visible to peers sharing the same ProjectCode; the bucket's
// CAS semantics provide the consensus that an in-memory LockSystem can't.
//
// V1 scope: exclusive (non-shared) locks, depth-0 (single file). Most
// WebDAV clients in the editing path (Office, VS Code) only use these.
type natsLockSystem struct {
	kv      jetstream.KeyValue
	agentID string
	opTimeout time.Duration

	mu     sync.Mutex
	tokens map[string]localLock // our-side index, token → KV key + rev
}

type localLock struct {
	Key      string
	Revision uint64
	Root     string
	Expiry   time.Time
}

// kvLockValue is the JSON shape of each lock entry stored in the KV.
type kvLockValue struct {
	Token  string    `json:"token"`
	Owner  string    `json:"owner"`
	Root   string    `json:"root"`
	Expiry time.Time `json:"expiry"`
}

const lockTokenPrefix = "opaquelocktoken:vfs:"

// newNATSLockSystem opens/creates a JetStream KV bucket for the project
// and returns a webdav.LockSystem backed by it.
func newNATSLockSystem(ctx context.Context, nc *nats.Conn, projectCode, agentID string) (*natsLockSystem, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("vfs locks: jetstream context: %w", err)
	}
	bucketName := "fossil-vfs-locks-" + sanitiseBucketName(projectCode)
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  bucketName,
		History: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("vfs locks: kv bucket %q: %w", bucketName, err)
	}
	return &natsLockSystem{
		kv:        kv,
		agentID:   agentID,
		opTimeout: 5 * time.Second,
		tokens:    map[string]localLock{},
	}, nil
}

// opCtx returns a fresh context with the configured per-op timeout. KV
// operations must use this rather than a stored context — the latter
// gets cancelled when the constructor returns.
func (n *natsLockSystem) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), n.opTimeout)
}

// pathToKey encodes any path string to a KV-safe key.
func pathToKey(p string) string {
	if p == "" {
		p = "."
	}
	return base64.RawURLEncoding.EncodeToString([]byte(p))
}

func keyToPath(k string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(k)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// makeToken bundles the KV key into the token string so Unlock and Refresh
// can find the entry directly. The agent ID is included for auditing.
func (n *natsLockSystem) makeToken(key string) string {
	return lockTokenPrefix + key + ":" + n.agentID
}

func parseToken(token string) (key, agentID string, ok bool) {
	if !strings.HasPrefix(token, lockTokenPrefix) {
		return "", "", false
	}
	rest := token[len(lockTokenPrefix):]
	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

func (n *natsLockSystem) Confirm(now time.Time, name0, name1 string, conditions ...webdav.Condition) (func(), error) {
	// Permissive confirm: if any supplied condition references one of our
	// known tokens for the requested name(s), accept. This matches the
	// behaviour MemLS provides and the practical needs of common WebDAV
	// clients.
	if len(conditions) == 0 {
		return func() {}, nil
	}
	ctx, cancel := n.opCtx()
	defer cancel()
	for _, name := range []string{name0, name1} {
		if name == "" {
			continue
		}
		key := pathToKey(name)
		entry, err := n.kv.Get(ctx, key)
		if err != nil {
			// Not locked at the KV layer — nothing to confirm.
			continue
		}
		var v kvLockValue
		if err := json.Unmarshal(entry.Value(), &v); err != nil {
			continue
		}
		if now.After(v.Expiry) {
			continue
		}
		found := false
		for _, c := range conditions {
			if c.Token == v.Token {
				found = true
				break
			}
		}
		if !found {
			return nil, webdav.ErrConfirmationFailed
		}
	}
	return func() {}, nil
}

func (n *natsLockSystem) Create(now time.Time, details webdav.LockDetails) (string, error) {
	if details.ZeroDepth == false && details.Root != "" {
		// V1 only supports depth=0 (no recursive locks). Reject otherwise.
		// Most editors only request depth=0 anyway.
		return "", errors.New("vfs locks: recursive locks not supported in V1")
	}
	key := pathToKey(details.Root)
	expiry := now.Add(details.Duration)
	ctx, cancel := n.opCtx()
	defer cancel()

	// First try to read existing. If found and not expired → conflict.
	if existing, err := n.kv.Get(ctx, key); err == nil {
		var v kvLockValue
		if jsErr := json.Unmarshal(existing.Value(), &v); jsErr == nil {
			if now.Before(v.Expiry) {
				return "", webdav.ErrLocked
			}
			// Expired — replace via CAS Update against the existing rev.
			token := n.makeToken(key)
			v = kvLockValue{Token: token, Owner: n.agentID, Root: details.Root, Expiry: expiry}
			data, _ := json.Marshal(v)
			rev, err := n.kv.Update(ctx, key, data, existing.Revision())
			if err != nil {
				return "", fmt.Errorf("vfs locks: replace expired: %w", err)
			}
			n.recordToken(token, key, rev, details.Root, expiry)
			return token, nil
		}
	}

	// Atomic create: fails if a peer slipped in between our Get and Create.
	token := n.makeToken(key)
	v := kvLockValue{Token: token, Owner: n.agentID, Root: details.Root, Expiry: expiry}
	data, _ := json.Marshal(v)
	rev, err := n.kv.Create(ctx, key, data)
	if err != nil {
		if isAlreadyExists(err) {
			return "", webdav.ErrLocked
		}
		return "", fmt.Errorf("vfs locks: create: %w", err)
	}
	n.recordToken(token, key, rev, details.Root, expiry)
	return token, nil
}

func (n *natsLockSystem) Refresh(now time.Time, token string, duration time.Duration) (webdav.LockDetails, error) {
	key, owner, ok := parseToken(token)
	if !ok {
		return webdav.LockDetails{}, webdav.ErrNoSuchLock
	}
	ctx, cancel := n.opCtx()
	defer cancel()
	entry, err := n.kv.Get(ctx, key)
	if err != nil {
		return webdav.LockDetails{}, webdav.ErrNoSuchLock
	}
	var v kvLockValue
	if err := json.Unmarshal(entry.Value(), &v); err != nil {
		return webdav.LockDetails{}, webdav.ErrNoSuchLock
	}
	if v.Owner != owner {
		return webdav.LockDetails{}, webdav.ErrForbidden
	}
	v.Expiry = now.Add(duration)
	data, _ := json.Marshal(v)
	rev, err := n.kv.Update(ctx, key, data, entry.Revision())
	if err != nil {
		return webdav.LockDetails{}, fmt.Errorf("vfs locks: refresh: %w", err)
	}
	n.recordToken(token, key, rev, v.Root, v.Expiry)
	return webdav.LockDetails{
		Root:      v.Root,
		Duration:  duration,
		OwnerXML:  "",
		ZeroDepth: true,
	}, nil
}

func (n *natsLockSystem) Unlock(now time.Time, token string) error {
	key, owner, ok := parseToken(token)
	if !ok {
		return webdav.ErrNoSuchLock
	}
	ctx, cancel := n.opCtx()
	defer cancel()
	entry, err := n.kv.Get(ctx, key)
	if err != nil {
		return webdav.ErrNoSuchLock
	}
	var v kvLockValue
	if err := json.Unmarshal(entry.Value(), &v); err != nil {
		return webdav.ErrNoSuchLock
	}
	if v.Owner != owner {
		return webdav.ErrForbidden
	}
	if err := n.kv.Delete(ctx, key); err != nil {
		return fmt.Errorf("vfs locks: unlock: %w", err)
	}
	n.mu.Lock()
	delete(n.tokens, token)
	n.mu.Unlock()
	return nil
}

func (n *natsLockSystem) recordToken(token, key string, rev uint64, root string, expiry time.Time) {
	n.mu.Lock()
	n.tokens[token] = localLock{Key: key, Revision: rev, Root: root, Expiry: expiry}
	n.mu.Unlock()
}

// sanitiseBucketName scrubs a project-code into a valid JetStream bucket
// name (alphanumeric, '-', '_'). Most project codes are 40-char hex so
// they pass through unchanged.
func sanitiseBucketName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	// nats.go jetstream returns errors that wrap ErrKeyExists.
	if errors.Is(err, jetstream.ErrKeyExists) {
		return true
	}
	// fall through: string-match for resilience against renames.
	return strings.Contains(err.Error(), "key exists")
}
