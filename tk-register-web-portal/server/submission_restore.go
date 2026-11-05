package main

import (
	"context"
	"time"

	"goapi-tk-register/server/internal/core"
)

func (a *app) submissionService() core.VendorService {
	service := core.VendorService{Store: a.store, Transport: a.vendorTransport, Restorer: a.accountRestorer}
	if service.Restorer == nil && a.supplierClient != nil {
		service.Restorer = a.supplierClient
	}
	service.AcquireMutation = func(ctx context.Context, vendorID uint64, account string) (func(), error) {
		key := accountMutationLeaseKey(core.SyncedAccount{VendorID: vendorID, Account: account})
		token, err := randomToken()
		if err != nil {
			return nil, err
		}
		acquired, err := a.redis.SetNX(ctx, key, token, 150*time.Second).Result()
		if err != nil {
			return nil, err
		}
		if !acquired {
			return nil, &core.VendorError{Status: 409, Code: "ACCOUNT_UPDATE_RUNNING", Message: "An operation for this account is already running. Please wait."}
		}
		return func() { a.releaseDeletionLease(key, token) }, nil
	}
	return service
}
