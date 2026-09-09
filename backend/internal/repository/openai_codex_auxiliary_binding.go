package repository

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const codexAuxiliaryAccountBindingPrefix = "codex:auxiliary:account:v1:"

var resolveCodexAuxiliaryAccountBindingScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
local hadBinding = 0
if current ~= false then
  hadBinding = 1
  -- Compare canonical decimal strings, avoiding Lua number precision loss for
  -- int64 account IDs. Corruption must never silently select another account.
  if string.match(current, '^[1-9][0-9]*$') == nil or
      string.len(current) > 19 or
      (string.len(current) == 19 and current > '9223372036854775807') then
    return redis.error_reply('CODEX_AUXILIARY_INVALID_STORED_VALUE')
  end
  for i = 2, #ARGV do
    if current == ARGV[i] then
      return {current, 1, hadBinding}
    end
  end
end
local selected = ARGV[2]
for i = 2, #ARGV do
  if ARGV[1] == ARGV[i] then
    selected = ARGV[i]
    break
  end
end
redis.call('SET', KEYS[1], selected)
return {selected, 0, hadBinding}
`)

func (c *gatewayCache) ResolveCodexAuxiliaryAccountBinding(ctx context.Context, bindingKey string, eligibleAccountIDs []int64, preferredAccountID int64) (service.CodexAuxiliaryAccountBinding, error) {
	if err := service.ValidateCodexAuxiliaryAccountBindingRequest(bindingKey, eligibleAccountIDs, preferredAccountID); err != nil {
		return service.CodexAuxiliaryAccountBinding{}, err
	}
	if c == nil || c.rdb == nil {
		return service.CodexAuxiliaryAccountBinding{}, service.ErrCodexAuxiliaryAccountBindingStoreUnavailable
	}
	args := make([]any, 1, len(eligibleAccountIDs)+1)
	args[0] = strconv.FormatInt(preferredAccountID, 10)
	for _, accountID := range eligibleAccountIDs {
		args = append(args, strconv.FormatInt(accountID, 10))
	}
	result, err := resolveCodexAuxiliaryAccountBindingScript.Run(ctx, c.rdb, []string{codexAuxiliaryAccountBindingPrefix + bindingKey}, args...).Slice()
	if err != nil {
		if strings.Contains(err.Error(), "CODEX_AUXILIARY_INVALID_STORED_VALUE") || strings.Contains(err.Error(), "WRONGTYPE") {
			return service.CodexAuxiliaryAccountBinding{}, fmt.Errorf("%w: %v", service.ErrCodexAuxiliaryAccountBindingStoredInvalid, err)
		}
		return service.CodexAuxiliaryAccountBinding{}, err
	}
	if len(result) != 3 {
		return service.CodexAuxiliaryAccountBinding{}, service.ErrCodexAuxiliaryAccountBindingStoredInvalid
	}
	accountIDText, accountOK := result[0].(string)
	reused, reusedOK := result[1].(int64)
	hadBinding, hadBindingOK := result[2].(int64)
	accountID, parseErr := strconv.ParseInt(accountIDText, 10, 64)
	if !accountOK || !reusedOK || (reused != 0 && reused != 1) || !hadBindingOK || (hadBinding != 0 && hadBinding != 1) || reused > hadBinding || parseErr != nil || accountID <= 0 {
		return service.CodexAuxiliaryAccountBinding{}, service.ErrCodexAuxiliaryAccountBindingStoredInvalid
	}
	return service.CodexAuxiliaryAccountBinding{AccountID: accountID, Reused: reused == 1, HadBinding: hadBinding == 1}, nil
}

var _ service.CodexAuxiliaryAccountBindingStore = (*gatewayCache)(nil)
