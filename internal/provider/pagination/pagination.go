// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

// Package pagination provides a small, client-package-agnostic helper for
// fully paginating a "list" SSPI API operation, since every GetAll*
// operation across api_client/iam_client returns at most one page (bounded
// by the server's default page_size) unless the caller explicitly walks
// offset/page_size itself.
package pagination

// FetchAll repeatedly calls fetchPage, starting at offset 0 and advancing by
// the number of items each call returns, until either a call returns zero
// items or the running offset reaches totalResultCount. fetchPage is
// expected to call the specific GetAll* operation with the given offset and
// return that page's items, the API's reported total_result_count, and any
// error.
func FetchAll[T any](fetchPage func(offset int) (items []T, totalResultCount int, err error)) ([]T, error) {
	var all []T
	offset := 0
	for {
		items, total, err := fetchPage(offset)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		offset += len(items)
		if len(items) == 0 || offset >= total {
			return all, nil
		}
	}
}
