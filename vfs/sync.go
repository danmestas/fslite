package vfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/go-libfossil"
)

// SyncConfig configures NATS-mediated autosync between peer VFS instances
// sharing the same ProjectCode. Set on vfs.Config.Sync.
type SyncConfig struct {
	// NATS is the NATS connection used for notifications and the sync
	// transport. The caller owns it; VFS does not close it.
	NATS *nats.Conn
	// ProjectCode is shared across peer repos; it routes notification
	// and sync subjects. Conventionally the Fossil project-code.
	ProjectCode string
	// AgentID identifies this VFS instance on the bus. Used as the
	// suffix on the per-agent sync subject.
	AgentID string
	// PullTimeout bounds a single sync operation. Default 10s.
	PullTimeout time.Duration
}

func (c SyncConfig) commitSubject() string {
	return fmt.Sprintf("fossil-vfs.%s.commit", c.ProjectCode)
}

func (c SyncConfig) syncSubject(agentID string) string {
	return fmt.Sprintf("fossil-vfs.%s.sync.%s", c.ProjectCode, agentID)
}

// commitNotification is the JSON payload emitted on the commit subject
// after a successful local Commit.
type commitNotification struct {
	Agent string `json:"agent"`
	UUID  string `json:"uuid"`
	RID   int64  `json:"rid"`
}

// SyncRunner owns the NATS subscriptions and serves as the receiver
// side of the sync protocol. Created on demand by attachSync.
type SyncRunner struct {
	v   *VFS
	cfg SyncConfig

	notifSub *nats.Subscription
	syncSub  *nats.Subscription

	mu     sync.Mutex
	closed atomic.Bool
}

// attachSync wires up NATS subscriptions if Config.Sync is configured.
// Idempotent — calling twice on the same VFS is an error.
func (v *VFS) attachSync(cfg SyncConfig) error {
	if cfg.NATS == nil {
		return errors.New("vfs: SyncConfig.NATS is nil")
	}
	if cfg.ProjectCode == "" {
		return errors.New("vfs: SyncConfig.ProjectCode is required")
	}
	if cfg.AgentID == "" {
		return errors.New("vfs: SyncConfig.AgentID is required")
	}
	if cfg.PullTimeout == 0 {
		cfg.PullTimeout = 10 * time.Second
	}

	r := &SyncRunner{v: v, cfg: cfg}

	// Sync responder: peers send xfer payloads to <prefix>.sync.<our-id>;
	// libfossil.HandleSync produces the reply.
	syncSub, err := cfg.NATS.Subscribe(cfg.syncSubject(cfg.AgentID), r.handleSyncRequest)
	if err != nil {
		return fmt.Errorf("vfs: subscribe sync subject: %w", err)
	}
	r.syncSub = syncSub

	// Commit notifications: when a peer commits, react by pulling from them.
	notifSub, err := cfg.NATS.Subscribe(cfg.commitSubject(), r.handleCommitNotification)
	if err != nil {
		syncSub.Unsubscribe()
		return fmt.Errorf("vfs: subscribe commit subject: %w", err)
	}
	r.notifSub = notifSub

	v.sync = r

	// Spin up the cross-agent WebDAV lock system on the same NATS conn.
	// Best-effort — if JetStream isn't enabled on the server we keep
	// running, but WebDAV will fall back to MemLS (per-instance only).
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer lockCancel()
	ls, err := newNATSLockSystem(lockCtx, cfg.NATS, cfg.ProjectCode, cfg.AgentID)
	if err == nil {
		v.lockSystem = ls
	}
	return nil
}

// handleSyncRequest is the NATS responder for incoming xfer messages
// from a peer that's pulling from us. The reply is the encoded xfer
// response from libfossil.HandleSync.
func (r *SyncRunner) handleSyncRequest(msg *nats.Msg) {
	if r.closed.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.PullTimeout)
	defer cancel()

	resp, err := r.v.eng.Repo().HandleSync(ctx, msg.Data)
	if err != nil {
		_ = msg.Respond([]byte(fmt.Sprintf("ERR %v", err)))
		return
	}
	_ = msg.Respond(resp)
}

// handleCommitNotification is invoked when a peer announces a commit on
// the broadcast subject. We ignore our own notifications, then pull
// from the announcing peer.
func (r *SyncRunner) handleCommitNotification(msg *nats.Msg) {
	if r.closed.Load() {
		return
	}
	var n commitNotification
	if err := json.Unmarshal(msg.Data, &n); err != nil {
		return
	}
	if n.Agent == r.cfg.AgentID {
		return // our own notification
	}
	if err := r.PullFromPeer(n.Agent); err != nil {
		// Not fatal — peers will retry on subsequent commits, and the
		// next local Commit will re-sync.
		return
	}
}

// PullFromPeer initiates a fossil pull against the named peer agent
// via NATS request/reply over the sync subject. After a successful pull,
// the VFS's manifest snapshot is refreshed at the new tip.
func (r *SyncRunner) PullFromPeer(peerAgentID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.PullTimeout)
	defer cancel()

	transport := newNATSTransport(r.cfg.NATS, r.cfg.syncSubject(peerAgentID), r.cfg.PullTimeout)
	if _, err := r.v.eng.Repo().Sync(ctx, transport, libfossil.SyncOpts{Pull: true}); err != nil {
		return fmt.Errorf("vfs: pull from %s: %w", peerAgentID, err)
	}

	// Refresh manifest at the new tip.
	rid, err := r.v.eng.ResolveVersion("tip")
	if err != nil {
		return err
	}
	files, err := r.v.eng.ListFilesAt(rid)
	if err != nil {
		return err
	}

	r.v.viewMu.Lock()
	r.v.rid = rid
	r.v.manifestFiles = files
	r.v.modTime = time.Now()
	r.v.viewMu.Unlock()
	r.v.rebuildView()

	r.v.sizeMu.Lock()
	r.v.sizes = map[string]int64{}
	r.v.sizeMu.Unlock()
	return nil
}

// publishCommit notifies peers that we just committed. Called from
// VFS.Commit after the local commit succeeds.
func (r *SyncRunner) publishCommit(ck CheckinID) error {
	payload, err := json.Marshal(commitNotification{
		Agent: r.cfg.AgentID,
		UUID:  ck.UUID,
		RID:   ck.RID,
	})
	if err != nil {
		return err
	}
	return r.cfg.NATS.Publish(r.cfg.commitSubject(), payload)
}

func (r *SyncRunner) close() {
	r.closed.Store(true)
	if r.notifSub != nil {
		r.notifSub.Unsubscribe()
	}
	if r.syncSub != nil {
		r.syncSub.Unsubscribe()
	}
}

// natsTransport implements libfossil.Transport over NATS request/reply.
// Each RoundTrip publishes payload on the target subject and returns the
// reply bytes.
type natsTransport struct {
	nc      *nats.Conn
	subject string
	timeout time.Duration
}

func newNATSTransport(nc *nats.Conn, subject string, timeout time.Duration) *natsTransport {
	return &natsTransport{nc: nc, subject: subject, timeout: timeout}
}

func (t *natsTransport) RoundTrip(ctx context.Context, payload []byte) ([]byte, error) {
	deadline, ok := ctx.Deadline()
	wait := t.timeout
	if ok {
		wait = time.Until(deadline)
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
	}
	msg, err := t.nc.Request(t.subject, payload, wait)
	if err != nil {
		return nil, fmt.Errorf("nats request %s: %w", t.subject, err)
	}
	return msg.Data, nil
}
