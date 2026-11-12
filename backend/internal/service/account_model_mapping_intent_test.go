package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAccountModelMappingIntentScope(t *testing.T) {
	creds := map[string]any{"model_mapping": map[string]string{"a": "a"}}
	ctx := withAccountConfigurationIntent(context.Background(), []int64{7}, nil, nil, creds)
	delete(creds, "model_mapping")
	require.True(t, AccountConfigurationIntentFromContext(ctx, 7).ModelMapping)
	require.False(t, AccountConfigurationIntentFromContext(ctx, 8).ModelMapping)
	require.False(t, AccountConfigurationIntentFromContext(withAccountConfigurationIntent(context.Background(), []int64{7}, nil, nil), 7).ModelMapping)
	scoped := WithAccountModelMappingWriteIntent(context.Background(), []int64{9})
	require.True(t, AccountConfigurationIntentFromContext(scoped, 9).ModelMapping)
	require.False(t, AccountConfigurationIntentFromContext(scoped, 7).ModelMapping)
}
