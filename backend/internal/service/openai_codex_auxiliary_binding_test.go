package service

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalCodexAuxiliaryAccountBindingKeepsEligibleAccount(t *testing.T) {
	var bindings sync.Map
	key := strings.Repeat("a", 64)
	initial, err := resolveLocalCodexAuxiliaryAccountBinding(&bindings, key, []int64{11, 22}, 22)
	require.NoError(t, err)
	require.Equal(t, CodexAuxiliaryAccountBinding{AccountID: 22}, initial)

	kept, err := resolveLocalCodexAuxiliaryAccountBinding(&bindings, key, []int64{11, 22}, 11)
	require.NoError(t, err)
	require.Equal(t, CodexAuxiliaryAccountBinding{AccountID: 22, Reused: true, HadBinding: true}, kept)

	replaced, err := resolveLocalCodexAuxiliaryAccountBinding(&bindings, key, []int64{11, 33}, 44)
	require.NoError(t, err)
	require.Equal(t, CodexAuxiliaryAccountBinding{AccountID: 11, HadBinding: true}, replaced)
	other, err := resolveLocalCodexAuxiliaryAccountBinding(&bindings, strings.Repeat("b", 64), []int64{11, 33}, 33)
	require.NoError(t, err)
	require.Equal(t, CodexAuxiliaryAccountBinding{AccountID: 33}, other)

	kept, err = resolveLocalCodexAuxiliaryAccountBinding(&bindings, key, []int64{11, 22, 33}, 22)
	require.NoError(t, err)
	require.Equal(t, CodexAuxiliaryAccountBinding{AccountID: 11, Reused: true, HadBinding: true}, kept)
}

func TestLocalCodexAuxiliaryAccountBindingConcurrentSingleWinner(t *testing.T) {
	for _, replacing := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "replacement"}[replacing], func(t *testing.T) {
			var bindings sync.Map
			key := strings.Repeat("c", 64)
			if replacing {
				bindings.Store(key, int64(99))
			}
			type outcome struct {
				binding CodexAuxiliaryAccountBinding
				err     error
			}
			const count = 32
			start := make(chan struct{})
			results := make(chan outcome, count)
			for i := 0; i < count; i++ {
				go func(index int) {
					<-start
					binding, err := resolveLocalCodexAuxiliaryAccountBinding(&bindings, key, []int64{11, 22}, []int64{11, 22}[index%2])
					results <- outcome{binding, err}
				}(i)
			}
			close(start)
			var winner int64
			created := 0
			for i := 0; i < count; i++ {
				result := <-results
				require.NoError(t, result.err)
				if winner == 0 {
					winner = result.binding.AccountID
				}
				require.Equal(t, winner, result.binding.AccountID)
				require.Equal(t, replacing || result.binding.Reused, result.binding.HadBinding)
				if !result.binding.Reused {
					created++
				}
			}
			require.Equal(t, 1, created)
		})
	}
}

func TestLocalCodexAuxiliaryAccountBindingRejectsErrorsWithoutMutation(t *testing.T) {
	key := strings.Repeat("d", 64)
	for _, invalid := range []any{int64(0), int64(-1), "11", []int64{11}} {
		var bindings sync.Map
		bindings.Store(key, invalid)
		_, err := resolveLocalCodexAuxiliaryAccountBinding(&bindings, key, []int64{11, 22}, 22)
		require.ErrorIs(t, err, ErrCodexAuxiliaryAccountBindingStoredInvalid)
		stored, _ := bindings.Load(key)
		require.Equal(t, invalid, stored)
	}

	for name, request := range map[string]struct {
		key       string
		eligible  []int64
		preferred int64
	}{
		"empty key":          {"", []int64{11}, 0},
		"short key":          {strings.Repeat("a", 63), []int64{11}, 0},
		"uppercase key":      {strings.Repeat("A", 64), []int64{11}, 0},
		"non hex key":        {strings.Repeat("g", 64), []int64{11}, 0},
		"empty eligible":     {key, nil, 0},
		"zero account":       {key, []int64{11, 0}, 0},
		"negative account":   {key, []int64{-1, 11}, 0},
		"negative preferred": {key, []int64{11}, -1},
	} {
		t.Run(name, func(t *testing.T) {
			var bindings sync.Map
			bindings.Store(key, int64(11))
			_, err := resolveLocalCodexAuxiliaryAccountBinding(&bindings, request.key, request.eligible, request.preferred)
			require.ErrorIs(t, err, ErrCodexAuxiliaryAccountBindingInvalid)
			stored, _ := bindings.Load(key)
			require.Equal(t, int64(11), stored)
		})
	}
	_, err := resolveLocalCodexAuxiliaryAccountBinding(nil, key, []int64{11}, 0)
	require.ErrorIs(t, err, ErrCodexAuxiliaryAccountBindingStoreUnavailable)
}
