package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SilkageNet/codex-switch/internal/codexusage"
	"github.com/SilkageNet/codex-switch/internal/usagecache"
)

type usageView struct {
	Status     string                 `json:"status"`
	FetchedAt  time.Time              `json:"fetchedAt,omitempty"`
	PlanType   string                 `json:"planType,omitempty"`
	RateLimits *codexusage.RateLimits `json:"rateLimits,omitempty"`
	TokenUsage *codexusage.TokenUsage `json:"tokenUsage,omitempty"`
	Partial    []string               `json:"partial,omitempty"`
	Error      string                 `json:"error,omitempty"`
	ErrorAt    time.Time              `json:"errorAt,omitempty"`
	Attempts   int                    `json:"attempts,omitempty"`
}

const usageFreshness = 5 * time.Minute

func usageFromCache(cache usagecache.Cache, profileID, queryError string, now time.Time) *usageView {
	failure := cache.Failures[profileID]
	if queryError == "" {
		queryError = failure.Message
	}
	queryError = codexusage.NormalizeErrorMessage(queryError)
	if queryError != "" && failure.AttemptedAt.IsZero() {
		failure.AttemptedAt = now
	}
	snapshot, ok := cache.Profiles[profileID]
	if !ok {
		return &usageView{Status: "unavailable", Error: queryError, ErrorAt: failure.AttemptedAt, Attempts: failure.Attempts}
	}
	return &usageView{
		Status:     usageStatus(snapshot.FetchedAt, now),
		FetchedAt:  snapshot.FetchedAt,
		PlanType:   snapshot.PlanType,
		RateLimits: snapshot.RateLimits,
		TokenUsage: snapshot.TokenUsage,
		Partial:    snapshot.Partial,
		Error:      queryError,
		ErrorAt:    failure.AttemptedAt,
		Attempts:   failure.Attempts,
	}
}

func usageStatus(fetchedAt, now time.Time) string {
	if fetchedAt.IsZero() || now.Sub(fetchedAt) > usageFreshness {
		return "stale"
	}
	return "fresh"
}

func compactUsageError(usage *usageView) string {
	if usage == nil || usage.Error == "" {
		return "-"
	}
	value := strings.Join(strings.Fields(usage.Error), " ")
	const maxRunes = 56
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes-1]) + "…"
	}
	return value
}

func summarizeUsage(usage *usageView, now time.Time) (string, string, string, string, string) {
	if usage == nil || usage.Status == "unavailable" {
		return "-", "unavailable", "-", "-", "-"
	}
	plan := usage.PlanType
	if plan == "" {
		plan = "-"
	}
	limits := "-"
	if main := mainRateLimit(usage.RateLimits); main != nil {
		parts := make([]string, 0, 2)
		if main.Primary != nil {
			parts = append(parts, compactWindow(main.Primary, now))
		}
		if main.Secondary != nil {
			parts = append(parts, compactWindow(main.Secondary, now))
		}
		if len(parts) > 0 {
			limits = strings.Join(parts, " · ")
		}
	}
	resets := compactResetCredits(usage.RateLimits, now)
	tokens := "-"
	if usage.TokenUsage != nil && usage.TokenUsage.Summary.LifetimeTokens != nil {
		tokens = compactNumber(*usage.TokenUsage.Summary.LifetimeTokens)
	}
	updated := relativeTime(usage.FetchedAt, now)
	if usage.Status == "stale" {
		updated += " (stale)"
	}
	if usage.Error != "" {
		updated += " (error)"
	}
	return plan, limits, resets, tokens, updated
}

func formatUsage(view accountView, now time.Time) string {
	var output strings.Builder
	output.WriteString(formatView(view))
	if view.Active {
		output.WriteString(" [active]")
	}
	usage := view.Usage
	if usage == nil || usage.Status == "unavailable" {
		output.WriteString("\nUsage: unavailable")
		if usage != nil && usage.Error != "" {
			output.WriteString("\nError: ")
			output.WriteString(usage.Error)
		}
		return output.String()
	}
	plan := usage.PlanType
	if plan == "" {
		plan = "unknown"
	}
	fmt.Fprintf(&output, "\nPlan: %s", plan)
	fmt.Fprintf(&output, "\nUpdated: %s (%s)", usage.FetchedAt.Local().Format(time.RFC3339), usage.Status)
	if usage.RateLimits != nil {
		output.WriteString("\nLimits:")
		limits := usage.RateLimits.RateLimitsByLimitID
		if len(limits) == 0 {
			limits = map[string]codexusage.RateLimitSnapshot{"codex": usage.RateLimits.RateLimits}
		}
		keys := make([]string, 0, len(limits))
		for key := range limits {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			limit := limits[key]
			label := key
			if limit.LimitName != nil && *limit.LimitName != "" {
				label = *limit.LimitName
			}
			windows := make([]string, 0, 2)
			if limit.Primary != nil {
				windows = append(windows, detailedWindow(limit.Primary, now))
			}
			if limit.Secondary != nil {
				windows = append(windows, detailedWindow(limit.Secondary, now))
			}
			value := "unavailable"
			if len(windows) > 0 {
				value = strings.Join(windows, "; ")
			}
			fmt.Fprintf(&output, "\n  %s: %s", label, value)
		}
		formatResetCredits(&output, usage.RateLimits.RateLimitResetCredits, now)
	}
	if usage.TokenUsage != nil {
		summary := usage.TokenUsage.Summary
		parts := make([]string, 0, 5)
		if summary.LifetimeTokens != nil {
			parts = append(parts, compactNumber(*summary.LifetimeTokens)+" lifetime")
		}
		if summary.PeakDailyTokens != nil {
			parts = append(parts, compactNumber(*summary.PeakDailyTokens)+" peak/day")
		}
		if summary.CurrentStreakDays != nil {
			parts = append(parts, fmt.Sprintf("%dd current streak", *summary.CurrentStreakDays))
		}
		if summary.LongestStreakDays != nil {
			parts = append(parts, fmt.Sprintf("%dd longest streak", *summary.LongestStreakDays))
		}
		if summary.LongestRunningTurnSec != nil {
			parts = append(parts, (time.Duration(*summary.LongestRunningTurnSec)*time.Second).String()+" longest turn")
		}
		if len(parts) > 0 {
			output.WriteString("\nTokens: ")
			output.WriteString(strings.Join(parts, "; "))
		}
	}
	if len(usage.Partial) > 0 {
		output.WriteString("\nPartial: ")
		output.WriteString(strings.Join(usage.Partial, ", "))
	}
	if usage.Error != "" {
		output.WriteString("\nRefresh error")
		if usage.Attempts > 0 {
			fmt.Fprintf(&output, " after %d attempt(s)", usage.Attempts)
		}
		if !usage.ErrorAt.IsZero() {
			fmt.Fprintf(&output, " at %s", formatLocalTime(usage.ErrorAt))
		}
		output.WriteString(": ")
		output.WriteString(usage.Error)
	}
	return output.String()
}

func mainRateLimit(limits *codexusage.RateLimits) *codexusage.RateLimitSnapshot {
	if limits == nil {
		return nil
	}
	if value, ok := limits.RateLimitsByLimitID["codex"]; ok {
		copy := value
		return &copy
	}
	copy := limits.RateLimits
	return &copy
}

func compactWindow(window *codexusage.RateLimitWindow, now time.Time) string {
	value := compactWindowBase(window)
	if window.ResetsAt == nil {
		return value
	}
	reset := time.Unix(*window.ResetsAt, 0)
	if reset.After(now) {
		return value + " ↻" + compactDuration(reset.Sub(now))
	}
	return value + " ↻due"
}

func compactWindowBase(window *codexusage.RateLimitWindow) string {
	duration := "limit"
	if window.WindowDurationMins != nil {
		duration = compactMinutes(*window.WindowDurationMins)
	}
	return fmt.Sprintf("%s %d%%", duration, window.UsedPercent)
}

func detailedWindow(window *codexusage.RateLimitWindow, now time.Time) string {
	value := compactWindowBase(window) + " used"
	if window.ResetsAt != nil {
		reset := time.Unix(*window.ResetsAt, 0)
		if reset.After(now) {
			value += ", resets in " + compactDuration(reset.Sub(now)) + " at " + formatLocalTime(reset)
		} else {
			value += ", reset pending since " + formatLocalTime(reset)
		}
	}
	return value
}

func compactResetCredits(limits *codexusage.RateLimits, now time.Time) string {
	if limits == nil || limits.RateLimitResetCredits == nil {
		return "-"
	}
	credits := limits.RateLimitResetCredits
	value := strconv.FormatInt(credits.AvailableCount, 10)
	if credits.AvailableCount == 0 {
		return value
	}
	var earliest *time.Time
	for _, credit := range credits.Credits {
		if credit.ExpiresAt == nil || (credit.Status != "" && !strings.EqualFold(credit.Status, "available")) {
			continue
		}
		expires := time.Unix(*credit.ExpiresAt, 0)
		if earliest == nil || expires.Before(*earliest) {
			earliest = &expires
		}
	}
	if earliest == nil {
		return value
	}
	if earliest.After(now) {
		return value + " exp " + compactDuration(earliest.Sub(now))
	}
	return value + " exp due"
}

func formatResetCredits(output *strings.Builder, summary *codexusage.RateLimitResetCreditsSummary, now time.Time) {
	if summary == nil {
		return
	}
	fmt.Fprintf(output, "\nEarned resets: %d available", summary.AvailableCount)
	for _, credit := range summary.Credits {
		label := credit.ResetType
		if credit.Title != nil && *credit.Title != "" {
			label = *credit.Title
		}
		if label == "" {
			label = "Rate-limit reset"
		}
		details := make([]string, 0, 3)
		if credit.Status != "" {
			details = append(details, credit.Status)
		}
		if credit.ExpiresAt != nil {
			expires := time.Unix(*credit.ExpiresAt, 0)
			if expires.After(now) {
				details = append(details, "expires in "+compactDuration(expires.Sub(now))+" at "+formatLocalTime(expires))
			} else {
				details = append(details, "expired at "+formatLocalTime(expires))
			}
		} else {
			details = append(details, "expiration not provided")
		}
		if credit.GrantedAt > 0 {
			details = append(details, "granted "+formatLocalTime(time.Unix(credit.GrantedAt, 0)))
		}
		fmt.Fprintf(output, "\n  %s: %s", label, strings.Join(details, "; "))
	}
	if summary.AvailableCount > int64(len(summary.Credits)) {
		fmt.Fprintf(output, "\n  Details: Codex returned %d of %d available resets", len(summary.Credits), summary.AvailableCount)
	}
}

func formatLocalTime(value time.Time) string {
	return value.Local().Format("2006-01-02 15:04:05 MST")
}

func compactMinutes(minutes int64) string {
	if minutes%(60*24) == 0 {
		return fmt.Sprintf("%dd", minutes/(60*24))
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}

func compactDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration >= 24*time.Hour {
		return fmt.Sprintf("%dd %dh", int(duration/(24*time.Hour)), int(duration%(24*time.Hour)/time.Hour))
	}
	if duration >= time.Hour {
		return fmt.Sprintf("%dh %dm", int(duration/time.Hour), int(duration%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dm", int(duration/time.Minute))
}

func compactNumber(value int64) string {
	switch {
	case value >= 1_000_000_000:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", float64(value)/1_000_000_000), "0"), ".") + "B"
	case value >= 1_000_000:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", float64(value)/1_000_000), "0"), ".") + "M"
	case value >= 1_000:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", float64(value)/1_000), "0"), ".") + "K"
	default:
		return strconv.FormatInt(value, 10)
	}
}

func relativeTime(value, now time.Time) string {
	if value.IsZero() {
		return "never"
	}
	age := now.Sub(value)
	if age < 0 {
		age = 0
	}
	if age < time.Minute {
		return "just now"
	}
	return compactDuration(age) + " ago"
}
