// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type Key struct {
	Architecture string `json:"architecture"`
	Version      string `json:"version"`
}

func (k Key) String() string {
	return fmt.Sprintf("%s (%s)", k.Version, k.Architecture)
}

type Kernel struct {
	Architecture  string  `json:"architecture"`
	Version       string  `json:"version"`
	KernelVersion string  `json:"kernelVersion"`
	Modules       Modules `json:"modules"`
}

type Modules struct {
	Builtin  []string `json:"builtin"`
	Loadable []Module `json:"loadable"`
}

type Module struct {
	Name        string            `json:"name"`
	Compression ModuleCompression `json:"compression,omitempty"`
}

type ModuleCompression string

const (
	ModuleCompressionNone ModuleCompression = ""
	ModuleCompressionXZ   ModuleCompression = "xz"
)

func KeyFromKernel(k *Kernel) Key {
	return Key{
		Architecture: k.Architecture,
		Version:      k.Version,
	}
}

func CompareKeys(a, b Key) int {
	return cmp.Or(
		cmp.Compare(a.Architecture, b.Architecture),
		cmp.Compare(a.Version, b.Version),
	)
}

type ListOptions struct {
	// Architecture to filter kernels by.
	Architecture string
}

var (
	ErrNotFound = fmt.Errorf("not found")
)

type Bundle interface {
	io.Reader
	Next() (*BundleItem, error)
}

type BundleCloser interface {
	Bundle
	io.Closer
}

type Reader interface {
	List(ctx context.Context, opts ListOptions) ([]Key, error)
	Inspect(ctx context.Context, key Key) (*Kernel, error)
	// FetchBinary fetches the kernel binary specified by the given key.
	FetchBinary(ctx context.Context, key Key) (io.ReadCloser, error)
	// FetchModule fetches the kernel module specified by the given key and module name.
	FetchModule(ctx context.Context, key Key, module string) (io.ReadCloser, error)

	FetchBundle(ctx context.Context, key Key) (BundleCloser, error)
}

type BundleItemType uint

const (
	BundleItemTypeUnknown BundleItemType = iota
	BundleItemTypeBinary
	BundleItemTypeModule
	BundleItemTypeModuleXz
	BundleItemTypeManifest
)

type BundleItem struct {
	Type BundleItemType
	Name string
}

func ReadManifest(data []byte) (*Kernel, error) {
	k := &Kernel{}
	if err := json.Unmarshal(data, k); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	return k, nil
}

func WriteManifest(k *Kernel) ([]byte, error) {
	return json.Marshal(k)
}

type Writer interface {
	WriteBundle(ctx context.Context, key Key, b Bundle) error
}

func Copy(ctx context.Context, key Key, r Reader, w Writer) error {
	b, err := r.FetchBundle(ctx, key)
	if err != nil {
		return fmt.Errorf("fetch bundle: %w", err)
	}

	return w.WriteBundle(ctx, key, b)
}

type ReadWriter interface {
	Reader
	Writer
}
