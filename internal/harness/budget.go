package harness

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A stochastic process can loop: re-read the same files, rewrite the same
// file, fetch the same page, or talk until the bill arrives. Budgets bound
// what one run may consume, the way rlimits bound a process. A run and the
// child agents it spawns draw on one meter, so delegating work cannot reset
// the count.

// Budget is a run's resource limits. Zero means the default (see
// effectiveBudget); a negative value means unlimited.
type Budget struct {
	MaxTokens      int           `json:"max_tokens,omitempty"`       // prompt + completion, whole run
	MaxCostUSD     float64       `json:"max_cost_usd,omitempty"`     // estimated, whole run
	MaxDuration    time.Duration `json:"max_duration,omitempty"`     // wall clock, whole run
	MaxWrites      int           `json:"max_writes,omitempty"`       // file-tool mutations
	MaxWriteBytes  int64         `json:"max_write_bytes,omitempty"`  // bytes those mutations carry
	MaxWebRequests int           `json:"max_web_requests,omitempty"` // web_fetch + web_search
}

// Defaults apply to every run. Tokens and cost are unlimited by default:
// local models cost nothing, and a token count means little without knowing
// the model, so those are for the user to set.
const (
	defaultMaxDuration    = 30 * time.Minute
	defaultMaxWrites      = 500
	defaultMaxWriteBytes  = 50 << 20
	defaultMaxWebRequests = 100
	// maxBackgroundProcesses caps processes a session keeps running at once.
	maxBackgroundProcesses = 8
)

// effectiveBudget fills defaults and turns negative values into "unlimited" (0).
func effectiveBudget(b Budget) Budget {
	fillInt := func(v, def int) int {
		switch {
		case v < 0:
			return 0
		case v == 0:
			return def
		}
		return v
	}
	b.MaxTokens = fillInt(b.MaxTokens, 0)
	b.MaxWrites = fillInt(b.MaxWrites, defaultMaxWrites)
	b.MaxWebRequests = fillInt(b.MaxWebRequests, defaultMaxWebRequests)
	switch {
	case b.MaxWriteBytes < 0:
		b.MaxWriteBytes = 0
	case b.MaxWriteBytes == 0:
		b.MaxWriteBytes = defaultMaxWriteBytes
	}
	switch {
	case b.MaxDuration < 0:
		b.MaxDuration = 0
	case b.MaxDuration == 0:
		b.MaxDuration = defaultMaxDuration
	}
	if b.MaxCostUSD < 0 {
		b.MaxCostUSD = 0
	}
	return b
}

// budgetMeter is what a run tree has consumed. A nil meter records nothing.
type budgetMeter struct {
	mu          sync.Mutex
	limits      Budget // effective
	started     time.Time
	tokens      int
	cost        float64
	costUnknown bool
	writes      int
	writeBytes  int64
	web         int
}

func newBudgetMeter(b Budget) *budgetMeter {
	return &budgetMeter{limits: effectiveBudget(b), started: time.Now()}
}

// deadline is when the run tree must stop, or zero for none.
func (m *budgetMeter) deadline() time.Time {
	if m == nil || m.limits.MaxDuration == 0 {
		return time.Time{}
	}
	return m.started.Add(m.limits.MaxDuration)
}

// addTurn records one model call.
func (m *budgetMeter) addTurn(tm TurnMetrics, billable bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens += tm.TotalTokens
	m.cost += tm.EstimatedCost
	if billable && !tm.CostKnown {
		m.costUnknown = true
	}
}

// exhausted reports why the run must stop before another model call, or "".
func (m *budgetMeter) exhausted() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.limits
	switch {
	case l.MaxTokens > 0 && m.tokens >= l.MaxTokens:
		return fmt.Sprintf("token budget exhausted (%s of %s)", compactCount(m.tokens), compactCount(l.MaxTokens))
	case l.MaxCostUSD > 0 && m.costUnknown:
		return "cost budget cannot be enforced: this model's price is unknown"
	case l.MaxCostUSD > 0 && m.cost >= l.MaxCostUSD:
		return fmt.Sprintf("cost budget exhausted ($%.2f of $%.2f)", m.cost, l.MaxCostUSD)
	case l.MaxDuration > 0 && time.Since(m.started) >= l.MaxDuration:
		return fmt.Sprintf("time budget exhausted (%s)", l.MaxDuration)
	}
	return ""
}

// charge accounts for one tool call before it runs and returns a refusal for
// the model when it would exceed a limit. Refused calls are not counted.
func (m *budgetMeter) charge(tool, args string) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.limits
	switch toolClasses[tool] {
	case classWrite:
		size := int64(writeSize(tool, args))
		if l.MaxWrites > 0 && m.writes+1 > l.MaxWrites {
			return budgetRefusal(fmt.Sprintf("this run has made its limit of %d file changes", l.MaxWrites))
		}
		if l.MaxWriteBytes > 0 && m.writeBytes+size > l.MaxWriteBytes {
			return budgetRefusal(fmt.Sprintf("this change would exceed the run's %s write budget", formatBytes(l.MaxWriteBytes)))
		}
		m.writes++
		m.writeBytes += size
	case classNetwork:
		if l.MaxWebRequests > 0 && m.web+1 > l.MaxWebRequests {
			return budgetRefusal(fmt.Sprintf("this run has made its limit of %d web requests", l.MaxWebRequests))
		}
		m.web++
	}
	return ""
}

func budgetRefusal(why string) string {
	return "Error: budget exhausted: " + why + ". Stop making this kind of call; finish with what you have and tell the user what remains."
}

// writeSize is the payload a mutation carries.
func writeSize(tool, args string) int {
	var parsed struct {
		Content string `json:"content"`
		Replace string `json:"replace"`
		Diff    string `json:"diff"`
	}
	_ = json.Unmarshal([]byte(args), &parsed)
	return len(parsed.Content) + len(parsed.Replace) + len(parsed.Diff)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// FormatBudget describes effective limits for /set and the runtime card.
func FormatBudget(b Budget) string {
	b = effectiveBudget(b)
	part := func(name string, set bool, value string) string {
		if !set {
			return name + " unlimited"
		}
		return name + " " + value
	}
	return strings.Join([]string{
		part("tokens", b.MaxTokens > 0, compactCount(b.MaxTokens)),
		part("cost", b.MaxCostUSD > 0, fmt.Sprintf("$%.2f", b.MaxCostUSD)),
		part("time", b.MaxDuration > 0, b.MaxDuration.String()),
		part("writes", b.MaxWrites > 0, fmt.Sprintf("%d / %s", b.MaxWrites, formatBytes(b.MaxWriteBytes))),
		part("web", b.MaxWebRequests > 0, fmt.Sprint(b.MaxWebRequests)),
	}, " · ")
}

// setBudgetValue applies /set tokens|cost|duration. "off" means unlimited.
func (m *teaModel) setBudgetValue(name, value string) error {
	off := value == "off" || value == "none" || value == "unlimited"
	switch name {
	case "tokens":
		if off {
			m.budget.MaxTokens = -1
			return nil
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.ReplaceAll(value, "_", ""), "k"))
		if err != nil || n <= 0 {
			return fmt.Errorf("tokens must be a positive number (e.g. 200000 or 200k) or off")
		}
		if strings.HasSuffix(value, "k") {
			n *= 1000
		}
		m.budget.MaxTokens = n
	case "cost":
		if off {
			m.budget.MaxCostUSD = -1
			return nil
		}
		usd, err := strconv.ParseFloat(strings.TrimPrefix(value, "$"), 64)
		if err != nil || usd <= 0 {
			return fmt.Errorf("cost must be a positive amount in USD (e.g. 2.50) or off")
		}
		m.budget.MaxCostUSD = usd
	case "duration":
		if off {
			m.budget.MaxDuration = -1
			return nil
		}
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("duration must be like 30m or 2h, or off")
		}
		m.budget.MaxDuration = d
	}
	return nil
}
