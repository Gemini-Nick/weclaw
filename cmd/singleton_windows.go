//go:build windows

package cmd

func acquireProcessSingleton() (func(), error) {
	return func() {}, nil
}
