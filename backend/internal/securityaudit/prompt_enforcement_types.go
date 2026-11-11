package securityaudit

import (
	"errors"
	"time"
)

var ErrEnforcementUnbanOwnership = errors.New("user is not disabled by prompt auditing")
var ErrEnforcementUserNotFound = errors.New("prompt audit user not found")
var ErrEnforcementNotificationUnavailable = errors.New("prompt enforcement notification unavailable")
var ErrEnforcementNotificationLeaseLost = errors.New("prompt enforcement notification lease lost")

type EnforcementPolicy struct {
	AutoBanEnabled       bool
	ViolationWindowHours int
	BanThreshold         int
	EmailOnHit           bool
	EmailMergeMinutes    int
}

type EnforcementInput struct {
	Snapshot      PromptSnapshot
	ConfigVersion int64
	Result        *NormalizedResult
	RequestKey    string
	StartedAt     time.Time
	Kind          string
	RuleGroupName string
	Policy        EnforcementPolicy
}

// EnforcementOutcome contains committed facts, never rule text or prompt content.
type EnforcementOutcome struct {
	Recorded       bool   `json:"recorded"`
	Counted        bool   `json:"counted"`
	Kind           string `json:"kind"`
	ViolationCount int    `json:"violation_count"`
	WindowHours    int    `json:"window_hours"`
	BanThreshold   int    `json:"ban_threshold"`
	AutoBanned     bool   `json:"auto_banned"`
	Epoch          int64  `json:"epoch"`
	EmailStatus    string `json:"email_status"`
	UserStatus     string `json:"user_status"`
	CanUnban       bool   `json:"can_unban"`
	Reason         string `json:"reason,omitempty"`
	UserChanged    bool   `json:"-"`
}

type EnforcementNotification struct {
	ID              int64
	ClaimVersion    int64
	ClaimedBy       string
	DeliveryVersion int64
	UserID          int64
	Epoch           int64
	Email           string
	Username        string
	UserStatus      string
	Kind            string
	Categories      []string
	RuleGroupNames  []string
	HitCount        int
	ViolationCount  int
	WindowHours     int
	BanThreshold    int
	AutoBanned      bool
	FirstHitAt      time.Time
	LastHitAt       time.Time
	Attempts        int
}
