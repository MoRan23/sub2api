//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

type quotaRedeemGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *quotaRedeemGroupRepo) GetByID(_ context.Context, id int64) (*Group, error) {
	g, ok := r.groups[id]
	if !ok {
		return nil, ErrGroupNotFound
	}
	return g, nil
}

type quotaRedeemRepo struct {
	RedeemCodeRepository
	codes        map[int64]*RedeemCode
	created      []RedeemCode
	batchUpdated bool
}

func (r *quotaRedeemRepo) Create(_ context.Context, code *RedeemCode) error {
	r.created = append(r.created, *code)
	return nil
}

func (r *quotaRedeemRepo) CreateBatch(_ context.Context, codes []RedeemCode) error {
	r.created = append(r.created, codes...)
	return nil
}

func (r *quotaRedeemRepo) GetByID(_ context.Context, id int64) (*RedeemCode, error) {
	code := *r.codes[id]
	return &code, nil
}

func (r *quotaRedeemRepo) BatchUpdate(_ context.Context, ids []int64, _ RedeemCodeBatchUpdateFields) (int64, error) {
	r.batchUpdated = true
	return int64(len(ids)), nil
}

func quotaRedeemGroups() *quotaRedeemGroupRepo {
	return &quotaRedeemGroupRepo{groups: map[int64]*Group{
		1: {ID: 1, SubscriptionType: SubscriptionTypeSubscription},
		2: {ID: 2, SubscriptionType: SubscriptionTypeTotalQuota, TotalLimitUSD: new(999.0)},
		3: {ID: 3, SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true},
		4: {ID: 4, SubscriptionType: SubscriptionTypeStandard},
	}}
}

func TestValidateSubscriptionQuotaAmount(t *testing.T) {
	for _, amount := range []float64{0.00000001, 1, 12.34567891, 999999999999.1234} {
		require.NoError(t, ValidateSubscriptionQuotaAmount(&amount))
	}
	for _, amount := range []*float64{nil, new(0.0), new(-1.0), new(math.NaN()), new(math.Inf(1)), new(0.000000001), new(1.123456789), new(1e12)} {
		require.Error(t, ValidateSubscriptionQuotaAmount(amount))
	}
}

func TestValidateSubscriptionRedeemGrant(t *testing.T) {
	groups := quotaRedeemGroups().groups
	for _, tc := range []struct {
		name   string
		group  *Group
		amount *float64
		days   int
		valid  bool
	}{
		{"finite", groups[2], new(12.12345678), 30, true},
		{"finite_amount_required", groups[2], nil, 30, false},
		{"finite_duration_required", groups[2], new(12.0), 0, false},
		{"finite_negative_duration", groups[2], new(12.0), -1, false},
		{"permanent", groups[3], new(12.0), 0, true},
		{"permanent_amount_required", groups[3], nil, 0, false},
		{"window", groups[1], nil, 30, true},
		{"window_refund", groups[1], nil, -7, true},
		{"standard", groups[4], new(12.0), 30, false},
		{"missing_group", nil, new(12.0), 30, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSubscriptionRedeemGrant(tc.group, tc.amount, tc.days)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCreateTotalQuotaCodeFreezesGrantWithoutValue(t *testing.T) {
	for _, groupID := range []int64{2, 3} {
		repo := &quotaRedeemRepo{}
		svc := &RedeemService{redeemRepo: repo, subscriptionService: &SubscriptionService{groupRepo: quotaRedeemGroups()}}
		amount := 42.12345678
		code := &RedeemCode{Code: "external-order", Type: RedeemTypeSubscription, GroupID: &groupID, ValidityDays: 7, QuotaAmount: &amount}
		require.NoError(t, svc.CreateCode(context.Background(), code))
		amount = 99
		require.Len(t, repo.created, 1)
		require.Equal(t, 42.12345678, *repo.created[0].QuotaAmount)
		require.Zero(t, repo.created[0].Value)
		if groupID == 3 {
			require.Zero(t, repo.created[0].ValidityDays)
		} else {
			require.Equal(t, 7, repo.created[0].ValidityDays)
		}
	}
}

func TestGenerateTotalQuotaCodesRequirePerCodeAmount(t *testing.T) {
	groupID := int64(2)
	repo := &quotaRedeemRepo{}
	svc := &adminServiceImpl{groupRepo: quotaRedeemGroups(), redeemCodeRepo: repo}
	_, err := svc.GenerateRedeemCodes(context.Background(), &GenerateRedeemCodesInput{Count: 2, Type: RedeemTypeSubscription, GroupID: &groupID, ValidityDays: 30})
	require.ErrorContains(t, err, "quota_amount")
	require.Empty(t, repo.created)
	amount := 25.0
	codes, err := svc.GenerateRedeemCodes(context.Background(), &GenerateRedeemCodesInput{Count: 2, Type: RedeemTypeSubscription, GroupID: &groupID, ValidityDays: 30, QuotaAmount: &amount})
	require.NoError(t, err)
	require.Len(t, codes, 2)
	amount = 99
	require.Equal(t, 25.0, *codes[0].QuotaAmount)
	require.Equal(t, 25.0, *codes[1].QuotaAmount)
	*codes[0].QuotaAmount = 50
	require.Equal(t, 25.0, *codes[1].QuotaAmount)
}

func TestGenerateSubscriptionCodeDurations(t *testing.T) {
	for _, tc := range []struct {
		groupID  int64
		days     int
		wantDays int
		valid    bool
	}{
		{1, 0, 30, true}, {2, 0, 0, false}, {2, 14, 14, true}, {3, 0, 0, true}, {3, 14, 0, true},
	} {
		repo := &quotaRedeemRepo{}
		svc := &adminServiceImpl{groupRepo: quotaRedeemGroups(), redeemCodeRepo: repo}
		codes, err := svc.GenerateRedeemCodes(context.Background(), &GenerateRedeemCodesInput{Count: 1, Type: RedeemTypeSubscription, GroupID: &tc.groupID, ValidityDays: tc.days, QuotaAmount: new(8.0)})
		if !tc.valid {
			require.Error(t, err)
			require.Empty(t, repo.created)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tc.wantDays, codes[0].ValidityDays)
		if tc.groupID == 1 {
			require.Nil(t, codes[0].QuotaAmount)
		}
	}
}

func TestRedeemBatchEnableAndGroupChangeValidateFrozenGrant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		amount *float64
		days   int
		from   int64
		to     *int64
		valid  bool
	}{
		{"legacy_cannot_enable", nil, 30, 2, nil, false},
		{"finite_enable", new(25.0), 30, 2, nil, true},
		{"permanent_enable", new(25.0), 0, 3, nil, true},
		{"finite_to_permanent", new(25.0), 30, 2, new(int64(3)), true},
		{"finite_to_window", new(25.0), 30, 2, new(int64(1)), true},
		{"permanent_to_window_missing_days", new(25.0), 0, 3, new(int64(1)), false},
		{"permanent_to_finite_missing_days", new(25.0), 0, 3, new(int64(2)), false},
		{"window_to_quota_missing_amount", nil, 30, 1, new(int64(2)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &quotaRedeemRepo{codes: map[int64]*RedeemCode{10: {ID: 10, Type: RedeemTypeSubscription, Status: StatusDisabled, GroupID: &tc.from, ValidityDays: tc.days, QuotaAmount: tc.amount}}}
			svc := &RedeemService{redeemRepo: repo, subscriptionService: &SubscriptionService{groupRepo: quotaRedeemGroups()}}
			fields := RedeemCodeBatchUpdateFields{Status: new(StatusUnused)}
			if tc.to != nil {
				fields.GroupID = NullableInt64Update{Set: true, Value: tc.to}
			}
			_, err := svc.BatchUpdate(context.Background(), &RedeemCodeBatchUpdateInput{IDs: []int64{10}, Fields: fields})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, tc.valid, repo.batchUpdated)
		})
	}
}

func TestRedeemPreparePreservesLegacyWindowDefault(t *testing.T) {
	svc := &RedeemService{subscriptionService: &SubscriptionService{groupRepo: quotaRedeemGroups()}}
	code := &RedeemCode{Type: RedeemTypeSubscription, GroupID: new(int64(1))}
	require.NoError(t, svc.prepareSubscriptionCode(context.Background(), code, true))
	require.Equal(t, 30, code.ValidityDays)
	code = &RedeemCode{Type: RedeemTypeSubscription, GroupID: new(int64(2))}
	require.ErrorContains(t, svc.prepareSubscriptionCode(context.Background(), code, true), "quota_amount")
}

func TestRedeemBatchQuotaAmountIsImmutable(t *testing.T) {
	repo := &quotaRedeemRepo{}
	svc := &RedeemService{redeemRepo: repo}
	_, err := svc.BatchUpdate(context.Background(), &RedeemCodeBatchUpdateInput{
		IDs: []int64{1}, Fields: RedeemCodeBatchUpdateFields{QuotaAmount: new(12.0)},
	})
	require.ErrorContains(t, err, "cannot be batch updated")
	require.False(t, repo.batchUpdated)
}

func TestGenerateCodesFreezesQuotaAndIgnoresUnrelatedGrantFields(t *testing.T) {
	repo := &quotaRedeemRepo{}
	svc := &RedeemService{redeemRepo: repo, subscriptionService: &SubscriptionService{groupRepo: quotaRedeemGroups()}}
	amount := 7.12345678
	codes, err := svc.GenerateCodes(context.Background(), GenerateCodesRequest{
		Count: 2, Type: RedeemTypeSubscription, GroupID: new(int64(3)), QuotaAmount: &amount,
	})
	require.NoError(t, err)
	amount = 10
	require.Equal(t, 7.12345678, *codes[0].QuotaAmount)
	require.Equal(t, 7.12345678, *codes[1].QuotaAmount)
	require.Zero(t, codes[0].ValidityDays)
	codes, err = svc.GenerateCodes(context.Background(), GenerateCodesRequest{
		Count: 1, Type: RedeemTypeBalance, Value: 5, GroupID: new(int64(3)), QuotaAmount: &amount, ValidityDays: 9,
	})
	require.NoError(t, err)
	require.Nil(t, codes[0].QuotaAmount)
	require.Nil(t, codes[0].GroupID)
	require.Zero(t, codes[0].ValidityDays)
}

type quotaRedeemCache struct {
	BillingCache
	deleted   []int64
	published string
	ctxErr    error
}

func (c *quotaRedeemCache) InvalidateSubscriptionCache(ctx context.Context, userID, groupID int64) error {
	c.deleted = []int64{userID, groupID}
	c.ctxErr = ctx.Err()
	return nil
}

func (c *quotaRedeemCache) PublishSubscriptionCacheInvalidation(_ context.Context, key string) error {
	c.published = key
	return nil
}

func (c *quotaRedeemCache) SubscribeSubscriptionCacheInvalidation(context.Context, func(string)) error {
	return nil
}

func TestRedeemSubscriptionInvalidatesAllInstancesAfterCommit(t *testing.T) {
	cache := &quotaRedeemCache{}
	billing := &BillingCacheService{cache: cache}
	svc := &RedeemService{subscriptionService: &SubscriptionService{billingCacheService: billing}, billingCacheService: billing}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.invalidateRedeemCaches(ctx, 17, &RedeemCode{Type: RedeemTypeSubscription, GroupID: new(int64(19))})
	require.Equal(t, []int64{17, 19}, cache.deleted)
	require.Equal(t, subCacheKey(17, 19), cache.published)
	require.NoError(t, cache.ctxErr)
}
