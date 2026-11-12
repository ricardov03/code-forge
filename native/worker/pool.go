package worker

import (
	"errors"
	"io"
	"math"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/session"
)

// ExpiryCheckFailed is loop.mjs EXPIRY_CHECK_FAILED (B48).
const ExpiryCheckFailed = "the autopilot expiry check failed in a worker drain; it runs again at the next drain"

// FileKey is a pending ticket's scheduling hint: its block and the
// (block, file) lock key (loop.mjs fileLockKey).
type FileKey struct {
	Key   string
	Block string
}

// FileLockKey is loop.mjs fileLockKey.
func FileLockKey(block, file string) string { return block + "\x00" + file }

// Pool is the B42 ticket pool and the per-provider session caps of one
// worker (loop.mjs drain, spawnPooled, dispatchSessions, poolSettings).
type Pool struct {
	Root   string
	Run    string
	PollMs int
	// BootCfg sets the session ticket limit until the first drain reads settings.
	BootCfg map[string]any
	// RunOne runs one ticket and never panics: 1 when completed, else 0;
	// poison skips the ticket for this worker's life.
	RunOne func(id string) (completed int, poison bool)
	// Settings is the config in force (the snapshot, else the boot config)
	// and the run record it came from; read once per drain with work.
	Settings func() (cfg map[string]any, record any)
	// FileKey reads a pending ticket's hint; ok false when unreadable (not cached).
	FileKey func(id string) (FileKey, bool)
	// ProviderHint is the first reviewer's provider, "" when unknown.
	ProviderHint func(id string, record any) string
	ExpiryCheck  func() error
	WatchTick    func(active map[string]bool) error
	// Warn writes one warning-log row (logWarning); failures are ignored.
	Warn   func(warning, message string)
	Stderr io.Writer
	// GuardPath: the authoritative run record; its removal stops the worker.
	GuardPath string
	Claim     func(run, id string) (release func(), ok bool, err error)
	Stopping  func() bool
	Stop      func()
	Now       func() time.Time

	mu               sync.Mutex
	poisoned         map[string]bool
	fileKeys         map[string]FileKey
	expiryFailing    bool
	ticketLimit      int
	providerParallel int
	tickets          map[string]*ticketSessions
	providerActive   map[string]int
	queue            []*sessionWaiter
	wake             chan struct{}
	active           map[string]bool
	// Activity counts observed ticket work (idle backoff and idle exit).
	Activity         int
	LastActivity     time.Time
	TicketSinceCheck bool
}

type ticketSessions struct{ active, pending int }

type sessionWaiter struct {
	id, provider string
	ready        chan struct{}
}

func (p *Pool) init() {
	if p.poisoned == nil {
		p.poisoned = map[string]bool{}
		p.fileKeys = map[string]FileKey{}
		p.tickets = map[string]*ticketSessions{}
		p.providerActive = map[string]int{}
		p.active = map[string]bool{}
		p.wake = make(chan struct{}, 1)
		if p.ticketLimit == 0 {
			p.ticketLimit = ParallelTickets(p.BootCfg)
		}
		if p.providerParallel == 0 {
			p.providerParallel = DefaultProviderParallel
		}
	}
}

func (p *Pool) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pool) stopping() bool { return p.Stopping != nil && p.Stopping() }

// Wake is poolWake: it ends the drain's current wait (stop, a session start).
func (p *Pool) Wake() {
	p.mu.Lock()
	p.init()
	wake := p.wake
	p.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (p *Pool) warn(text, warning string) {
	if p.Stderr != nil {
		_, _ = io.WriteString(p.Stderr, "worker: WARN "+text+"\n")
	}
	if p.Warn != nil {
		p.Warn(warning, text)
	}
}

// Poisoned reports whether a ticket is skipped for this worker's life.
func (p *Pool) Poisoned(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	return p.poisoned[id]
}

// Limits are the pool's current ticket and provider caps.
func (p *Pool) Limits() (tickets, provider int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	return p.ticketLimit, p.providerParallel
}

func (p *Pool) settings() (limit int, fair bool, record any) {
	var cfg map[string]any
	if p.Settings != nil {
		cfg, record = p.Settings()
	}
	p.mu.Lock()
	p.init()
	p.ticketLimit = ParallelTickets(cfg)
	p.providerParallel = ProviderParallel(cfg)
	p.mu.Unlock()
	return ParallelTickets(cfg), FairShare(cfg), record
}

// SessionProvider is spawnPooled's provider: levels[level].provider ??
// provider ?? "unresolved".
func SessionProvider(cfg map[string]any, level string) string {
	levels, _ := cfg["levels"].(map[string]any)
	l, _ := levels[level].(map[string]any)
	if s, ok := l["provider"]; ok && s != nil {
		if str, isString := s.(string); isString {
			return str
		}
	}
	if s, ok := cfg["provider"]; ok && s != nil {
		if str, isString := s.(string); isString {
			return str
		}
	}
	return "unresolved"
}

// SessionCfg caps review.provider_concurrency at review.provider_parallel for
// every session (fallback providers included); a config already stricter, or
// equal, is returned as is.
func (p *Pool) SessionCfg(cfg map[string]any) map[string]any {
	_, parallel := p.Limits()
	capped := map[string]any{}
	needs := false
	for _, provider := range []string{"anthropic", "openai", "xai"} {
		limit := session.ProviderLimit(cfg, provider)
		n := int(math.Min(float64(parallel), float64(limit)))
		capped[provider] = float64(n)
		if n < limit {
			needs = true
		}
	}
	if !needs {
		return cfg
	}
	out := make(map[string]any, len(cfg)+1)
	for k, v := range cfg {
		out[k] = v
	}
	review := map[string]any{}
	if r, ok := cfg["review"].(map[string]any); ok {
		for k, v := range r {
			review[k] = v
		}
	}
	review["provider_concurrency"] = capped
	out["review"] = review
	return out
}

// AcquireSession blocks until ticket id may start a session on provider:
// at most provider_parallel sessions per provider, and a ticket with no
// running session starts one only while fewer than parallel_tickets tickets
// have one. Waiting sessions keep their ticket's pool slot.
func (p *Pool) AcquireSession(id, provider string) (release func()) {
	p.mu.Lock()
	p.init()
	state := p.tickets[id]
	if state == nil {
		state = &ticketSessions{}
		p.tickets[id] = state
	}
	state.pending++
	w := &sessionWaiter{id: id, provider: provider, ready: make(chan struct{})}
	p.queue = append(p.queue, w)
	p.dispatchLocked()
	p.mu.Unlock()
	p.Wake()
	<-w.ready
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			state.active--
			p.providerActive[provider]--
			if state.active == 0 && state.pending == 0 && p.tickets[id] == state {
				delete(p.tickets, id)
			}
			p.dispatchLocked()
			p.mu.Unlock()
			p.Wake()
		})
	}
}

func (p *Pool) dispatchLocked() {
	for i := 0; i < len(p.queue); {
		next := p.queue[i]
		state := p.tickets[next.id]
		live := 0
		for _, s := range p.tickets {
			if s.active > 0 {
				live++
			}
		}
		if p.providerActive[next.provider] >= p.providerParallel || (state.active == 0 && live >= p.ticketLimit) {
			i++
			continue
		}
		p.queue = append(p.queue[:i], p.queue[i+1:]...)
		state.pending--
		state.active++
		p.providerActive[next.provider]++
		close(next.ready)
	}
}

// Active is the set of running tickets (the watch never supersedes them).
func (p *Pool) Active() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	out := make(map[string]bool, len(p.active))
	for id := range p.active {
		out[id] = true
	}
	return out
}

func (p *Pool) fileKeyOf(id string) (FileKey, bool) {
	p.mu.Lock()
	known, ok := p.fileKeys[id]
	p.mu.Unlock()
	if ok {
		return known, true
	}
	if p.FileKey == nil {
		return FileKey{}, false
	}
	key, ok := p.FileKey(id)
	if !ok {
		return FileKey{}, false
	}
	p.mu.Lock()
	p.fileKeys[id] = key
	p.mu.Unlock()
	return key, true
}

func (p *Pool) bump() {
	p.mu.Lock()
	p.Activity++
	p.LastActivity = p.now()
	p.TicketSinceCheck = true
	p.mu.Unlock()
}

type slotInfo struct {
	hasKey   bool
	key      FileKey
	provider string
}

type slotEnd struct {
	id     string
	n      int
	poison bool
}

// Drain serves every pending ticket (B42): up to parallel_tickets at once,
// oldest first, or fewest running per block with fair_share; a ticket whose
// (block, file) runs waits without a slot; each ticket starts at most once
// per drain; on stop no new ticket starts and the running ones are awaited.
// It returns how many tickets were completed.
func (p *Pool) Drain() (int, error) {
	p.mu.Lock()
	p.init()
	wake := p.wake
	p.mu.Unlock()
	// B48: the lazy autopilot checks before any ticket; never silent, warned once per streak.
	if p.ExpiryCheck != nil {
		if err := p.ExpiryCheck(); err != nil {
			if !p.expiryFailing {
				p.expiryFailing = true
				p.warn(ExpiryCheckFailed+" ("+ErrorKind(err)+")", "autopilot_expiry_check_failed")
			}
		} else {
			p.expiryFailing = false
		}
	}
	claim := p.Claim
	if claim == nil {
		claim = ClaimTicket
	}
	pollMs := p.PollMs
	if pollMs <= 0 {
		pollMs = DefaultPollMs
	}
	limit := -1
	fair := false
	var record any
	done := 0
	ended := make(chan slotEnd, MaxParallelTickets*4)
	slots := map[string]slotInfo{}
	busy := map[string]bool{}
	byBlock := map[string]int{}
	admitted := map[string]int{}
	started := map[string]bool{}
	finish := func(end slotEnd) {
		done += end.n
		info := slots[end.id]
		delete(slots, end.id)
		if info.hasKey {
			delete(busy, info.key.Key)
			byBlock[info.key.Block]--
			if byBlock[info.key.Block] <= 0 {
				delete(byBlock, info.key.Block)
			}
		}
		if info.provider != "" {
			admitted[info.provider]--
			if admitted[info.provider] <= 0 {
				delete(admitted, info.provider)
			}
		}
	}
	awaitAll := func() {
		for len(slots) > 0 {
			finish(<-ended)
		}
	}
	type hint struct {
		key FileKey
		ok  bool
	}
	for {
		if !p.stopping() && p.GuardPath != "" {
			if _, err := os.Stat(p.GuardPath); err != nil && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR)) {
				if p.Stop != nil {
					p.Stop()
				}
				if p.Warn != nil {
					p.Warn("worker_orphan_stop", "run record missing: "+p.GuardPath)
				}
			}
		}
		if !p.stopping() {
			if p.WatchTick != nil {
				if err := p.WatchTick(p.Active()); err != nil {
					p.warn("watch tick failed ("+ErrorKind(err)+")", "watch_tick_failed")
				}
			}
			var pending []string
			for _, id := range Pending(p.Root) {
				if !p.Poisoned(id) && !started[id] {
					pending = append(pending, id)
				}
			}
			if len(pending) > 0 || len(slots) > 0 {
				p.bump()
			}
			if len(pending) > 0 && limit < 0 {
				limit, fair, record = p.settings()
			}
			hints := map[string]hint{}
			providers := map[string]string{}
			for len(pending) > 0 && len(slots) < limit && !p.stopping() {
				next, fewest := -1, math.MaxInt
				_, parallel := p.Limits()
				for i, id := range pending {
					h, seen := hints[id]
					if !seen {
						key, ok := p.fileKeyOf(id)
						h = hint{key, ok}
						hints[id] = h
					}
					if h.ok && busy[h.key.Key] {
						continue // waits without a slot
					}
					provider, seenProvider := providers[id]
					if !seenProvider {
						if p.ProviderHint != nil {
							provider = p.ProviderHint(id, record)
						}
						providers[id] = provider
					}
					if provider != "" {
						p.mu.Lock()
						activeNow := p.providerActive[provider]
						p.mu.Unlock()
						if max(admitted[provider], activeNow) >= parallel {
							continue
						}
					}
					count := 0
					if fair && h.ok {
						count = byBlock[h.key.Block]
					}
					if count < fewest {
						next, fewest = i, count
					}
					if count == 0 {
						break // best possible count; ties stay oldest first
					}
				}
				if next == -1 {
					break
				}
				id := pending[next]
				pending = append(pending[:next], pending[next+1:]...)
				release, ok, err := claim(p.Run, id)
				if err != nil {
					awaitAll()
					return done, err
				}
				if !ok {
					continue // another process owns it; no pool slot is taken
				}
				if p.stopping() || IsDone(p.Root, id) {
					release()
					continue
				}
				h := hints[id]
				provider := providers[id]
				started[id] = true
				slots[id] = slotInfo{h.ok, h.key, provider}
				p.mu.Lock()
				p.active[id] = true
				p.mu.Unlock()
				if h.ok {
					busy[h.key.Key] = true
					byBlock[h.key.Block]++
				}
				if provider != "" {
					admitted[provider]++
				}
				go func(id string, release func()) {
					end := slotEnd{id: id}
					defer func() {
						release() // the done marker is published before the claim is released
						p.mu.Lock()
						delete(p.active, id)
						delete(p.fileKeys, id)
						if end.poison {
							p.poisoned[id] = true
						}
						p.mu.Unlock()
						ended <- end
					}()
					end.n, end.poison = p.RunOne(id)
				}(id, release)
			}
		}
		if len(slots) == 0 {
			break
		}
		timer := time.NewTimer(time.Duration(pollMs) * time.Millisecond)
		select {
		case end := <-ended:
			finish(end)
		case <-timer.C:
		case <-wake:
		}
		timer.Stop()
	}
	return done, nil
}
