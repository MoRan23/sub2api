//go:build unit

package repository

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// An explicit test DSN is required. Temporary tables are isolated to one
// connection and disappear on close; no application tables are touched.
func TestCatalogAndShadowProjectionsPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_SCHEDULER_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_SCHEDULER_PG_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `CREATE TEMP TABLE accounts(id bigint PRIMARY KEY,platform text,type text,status text,schedulable boolean,priority int,last_used_at timestamptz,expires_at timestamptz,auto_pause_on_expired boolean,rate_limit_reset_at timestamptz,overload_until timestamptz,temp_unschedulable_until timestamptz,parent_account_id bigint,updated_at timestamptz,deleted_at timestamptz,credentials jsonb,extra jsonb);CREATE TEMP TABLE account_groups(account_id bigint,group_id bigint,priority int);`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO accounts(id,platform,type,status,schedulable,priority,auto_pause_on_expired,updated_at,credentials,extra) VALUES(1,'openai','oauth','active',true,1,true,now(),'{"access_token":"private","model_mapping":{"alias":"target"}}','{"openai_passthrough":true,"test_answer":"private"}'),(2,'openai','oauth','disabled',true,1,false,now(),'{}','{}'),(3,'openai','apikey','active',false,1,false,now(),'{}','{}');INSERT INTO account_groups VALUES(1,7,1),(2,7,2),(3,7,3);`)
	require.NoError(t, err)
	repo := &accountRepository{sql: conn}
	accounts, err := repo.ListCatalogAccountProjections(ctx, 7)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Contains(t, accounts[0].Credentials, "model_mapping")
	require.NotContains(t, accounts[0].Credentials, "access_token")
	require.NotContains(t, accounts[0].Extra, "test_answer")
	require.Nil(t, accounts[0].Groups)
	require.Nil(t, accounts[0].Proxy)
	health, err := repo.GetShadowParentHealthBatch(ctx, []int64{1, 2, 99})
	require.NoError(t, err)
	require.Len(t, health, 2)
	require.Equal(t, "disabled", health[2].Status)
	require.True(t, health[1].AutoPauseOnExpired)
}
