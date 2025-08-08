package observer

import (
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Test utilities and common mock structures used across memoless tests

// MockThorClient implements a mock THORChain client for testing
type MockThorClient struct {
	memos map[string]*types.RegisteredMemo
	mu    sync.RWMutex
}

func NewMockThorClient() *MockThorClient {
	return &MockThorClient{
		memos: make(map[string]*types.RegisteredMemo),
	}
}

func (mtc *MockThorClient) GetRegisteredMemo(reference string) (*types.RegisteredMemo, error) {
	mtc.mu.RLock()
	defer mtc.mu.RUnlock()
	
	if memo, exists := mtc.memos[reference]; exists {
		return memo, nil
	}
	
	return nil, fmt.Errorf("memo not found for reference: %s", reference)
}

func (mtc *MockThorClient) SetMemo(reference string, memo *types.RegisteredMemo) {
	mtc.mu.Lock()
	defer mtc.mu.Unlock()
	mtc.memos[reference] = memo
}

// TestLogger provides a no-op logger for tests
type TestLogger struct{}

func NewTestLogger() zerolog.Logger {
	return zerolog.Nop()
}

// Common test types and enums

type ByzantineBehavior int

const (
	FalseMemoContent ByzantineBehavior = iota
	ConflictingMemos
	MemoNotFound
	DelayedResponse
	CorruptedData
)

type FaultType int

const (
	NetworkError FaultType = iota
	CorruptionError
	TimeoutError
)

type AttackType int

const (
	ReferenceCollision AttackType = iota
	AmountManipulation
	MemoInjection
	ReplayAttack
	TimingAttack
	ResourceExhaustion
	OracleManipulation
)

type Severity int

const (
	Low Severity = iota
	Medium
	High
	Critical
)

func (s Severity) String() string {
	switch s {
	case Low:
		return "Low"
	case Medium:
		return "Medium"
	case High:
		return "High"
	case Critical:
		return "Critical"
	default:
		return "Unknown"
	}
}

type OracleStrategy int

const (
	FalseMemoResponse OracleStrategy = iota
	DelayedOracleResponse
	CorruptedMetadata
	StaleDataResponse
)

// Test utility structures

type AdversarialScenario struct {
	name        string
	description string
	attackType  AttackType
	severity    Severity
	duration    time.Duration
}

type SecurityMetrics struct {
	startTime         time.Time
	attacksAttempted  int64
	attacksSuccessful int64
	vulnerabilities   map[string]int
	falsePositives    int64
	falseNegatives    int64
	mu                sync.Mutex
}

func (sm *SecurityMetrics) recordVulnerability(vulnType string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.vulnerabilities == nil {
		sm.vulnerabilities = make(map[string]int)
	}
	sm.vulnerabilities[vulnType]++
}

type TimingStats struct {
	samples int
	min     time.Duration
	max     time.Duration
	avg     time.Duration
}

type OracleManipulation struct {
	name     string
	strategy OracleStrategy
	payload  string
	delay    time.Duration
}

type DelayedResponse OracleStrategy

const (
	DelayedResponse OracleStrategy = iota + 4  // Continue from existing values
)