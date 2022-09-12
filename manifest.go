package regclient

import (
	"context"
	"fmt"

	"github.com/regclient/regclient/scheme"
	"github.com/regclient/regclient/types"
	"github.com/regclient/regclient/types/docker/schema2"
	"github.com/regclient/regclient/types/manifest"
	"github.com/regclient/regclient/types/platform"
	"github.com/regclient/regclient/types/ref"
)

type manifestOpt struct {
	d          types.Descriptor
	schemeOpts []scheme.ManifestOpts
}

// ManifestOpts define options for the Manifest* commands
type ManifestOpts func(*manifestOpt)

// WithManifest passes a manifest to ManifestDelete.
func WithManifest(m manifest.Manifest) ManifestOpts {
	return func(opts *manifestOpt) {
		opts.schemeOpts = append(opts.schemeOpts, scheme.WithManifest(m))
	}
}

// WithManifestCheckRefers checks for refers field on ManifestDelete.
func WithManifestCheckRefers() ManifestOpts {
	return func(opts *manifestOpt) {
		opts.schemeOpts = append(opts.schemeOpts, scheme.WithManifestCheckRefers())
	}
}

// WithManifestChild for ManifestPut.
func WithManifestChild() ManifestOpts {
	return func(opts *manifestOpt) {
		opts.schemeOpts = append(opts.schemeOpts, scheme.WithManifestChild())
	}
}

// WithManifestDesc includes the descriptor for ManifestGet.
// This is used to automatically extract a Data field if available.
func WithManifestDesc(d types.Descriptor) ManifestOpts {
	return func(opts *manifestOpt) {
		opts.d = d
	}
}

// ManifestDelete removes a manifest, including all tags pointing to that registry
// The reference must include the digest to delete (see TagDelete for deleting a tag)
// All tags pointing to the manifest will be deleted
func (rc *RegClient) ManifestDelete(ctx context.Context, r ref.Ref, opts ...ManifestOpts) error {
	opt := manifestOpt{schemeOpts: []scheme.ManifestOpts{}}
	for _, fn := range opts {
		fn(&opt)
	}
	schemeAPI, err := rc.schemeGet(r.Scheme)
	if err != nil {
		return err
	}
	return schemeAPI.ManifestDelete(ctx, r, opt.schemeOpts...)
}

// ManifestGet retrieves a manifest
func (rc *RegClient) ManifestGet(ctx context.Context, r ref.Ref, opts ...ManifestOpts) (manifest.Manifest, error) {
	opt := manifestOpt{schemeOpts: []scheme.ManifestOpts{}}
	for _, fn := range opts {
		fn(&opt)
	}
	if opt.d.Digest != "" {
		r.Digest = opt.d.Digest.String()
		data, err := opt.d.GetData()
		if err == nil {
			return manifest.New(
				manifest.WithDesc(opt.d),
				manifest.WithRaw(data),
				manifest.WithRef(r),
			)
		}
	}
	schemeAPI, err := rc.schemeGet(r.Scheme)
	if err != nil {
		return nil, err
	}
	return schemeAPI.ManifestGet(ctx, r)
}

// ManifestHead queries for the existence of a manifest and returns metadata (digest, media-type, size)
func (rc *RegClient) ManifestHead(ctx context.Context, r ref.Ref) (manifest.Manifest, error) {
	schemeAPI, err := rc.schemeGet(r.Scheme)
	if err != nil {
		return nil, err
	}
	return schemeAPI.ManifestHead(ctx, r)
}

// ManifestPut pushes a manifest
// Any descriptors referenced by the manifest typically need to be pushed first
func (rc *RegClient) ManifestPut(ctx context.Context, r ref.Ref, m manifest.Manifest, opts ...ManifestOpts) error {
	opt := manifestOpt{schemeOpts: []scheme.ManifestOpts{}}
	for _, fn := range opts {
		fn(&opt)
	}
	schemeAPI, err := rc.schemeGet(r.Scheme)
	if err != nil {
		return err
	}
	return schemeAPI.ManifestPut(ctx, r, m, opt.schemeOpts...)
}

func (rc *RegClient) truncatePlatformsFromManifest(ctx context.Context, refM ref.Ref, m manifest.Manifest, platforms []string, checkMissing bool) (manifest.Manifest, error) {
	if m == nil {
		return nil, nil
	}

	newManifestList := []types.Descriptor{}
	missingPlatforms := make(map[string]bool)
	for _, platformStr := range platforms {
		p, err := platform.Parse(platformStr)
		if err != nil {
			return nil, err
		}
		missingPlatforms[p.String()] = true
	}
	switch m.GetDescriptor().MediaType {
	case types.MediaTypeDocker2ManifestList:
		if manifestList, err := m.(manifest.Indexer).GetManifestList(); err != nil {
			return nil, err
		} else {
			for _, oldManifest := range manifestList {
				if ok, err := imagePlatformInList(oldManifest.Platform, platforms); err != nil {
					return nil, err
				} else if ok {
					missingPlatforms[oldManifest.Platform.String()] = false
					newManifestList = append(newManifestList, oldManifest)
				}
			}
		}
	case types.MediaTypeDocker2Manifest:

		configDescriptor, err := m.(manifest.Imager).GetConfig()
		if err != nil {
			return nil, err
		}
		blobConfig, err := rc.BlobGetOCIConfig(ctx, refM, configDescriptor)
		if err != nil {
			return nil, err
		}
		ociConfig := blobConfig.GetConfig()
		if ociConfig.OS == "" {
			return nil, nil
		}
		plat := platform.Platform{
			OS:           ociConfig.OS,
			Architecture: ociConfig.Architecture,
			OSVersion:    ociConfig.OSVersion,
			OSFeatures:   ociConfig.OSFeatures,
			Variant:      ociConfig.Variant,
			Features:     ociConfig.OSFeatures,
		}
		if ok, err := imagePlatformInList(&plat, platforms); err != nil {
			return nil, err
		} else if ok {
			missingPlatforms[plat.String()] = false
			newManifestList = append(newManifestList, m.GetDescriptor())
		}

	default:
		return nil, fmt.Errorf("operation is not implemented")
	}
	missingPlatformsList := []string{}
	for key, val := range missingPlatforms {
		if val {
			missingPlatformsList = append(missingPlatformsList, key)
		}
	}
	if checkMissing && len(missingPlatformsList) > 0 {
		return nil, fmt.Errorf("image %s is missing the following requested platforms: %v", refM.Reference, missingPlatformsList)
	}
	if len(newManifestList) == 0 {
		return nil, nil
	} else if len(newManifestList) == 1 {
		if newManifestList[0].Digest == m.GetDescriptor().Digest {
			return m, nil
		}
		return rc.ManifestGet(ctx, refM, WithManifestDesc(newManifestList[0]))
	} else {
		return manifest.New(manifest.WithOrig(schema2.ManifestList{
			Versioned: schema2.ManifestListSchemaVersion,
			Manifests: newManifestList,
		}))
	}
}
