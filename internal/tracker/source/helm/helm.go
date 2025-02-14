package helm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	"github.com/artifacthub/hub/internal/hub"
	"github.com/artifacthub/hub/internal/license"
	"github.com/artifacthub/hub/internal/pkg"
	"github.com/artifacthub/hub/internal/repo"
	"github.com/artifacthub/hub/internal/tracker/source"
	"github.com/artifacthub/hub/internal/util"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/deislabs/oras/pkg/content"
	ctxo "github.com/deislabs/oras/pkg/context"
	"github.com/deislabs/oras/pkg/oras"
	"github.com/hashicorp/go-multierror"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	helmrepo "helm.sh/helm/v3/pkg/repo"
)

const (
	concurrency = 10

	changesAnnotation              = "artifacthub.io/changes"
	crdsAnnotation                 = "artifacthub.io/crds"
	crdsExamplesAnnotation         = "artifacthub.io/crdsExamples"
	imagesAnnotation               = "artifacthub.io/images"
	licenseAnnotation              = "artifacthub.io/license"
	linksAnnotation                = "artifacthub.io/links"
	maintainersAnnotation          = "artifacthub.io/maintainers"
	operatorAnnotation             = "artifacthub.io/operator"
	operatorCapabilitiesAnnotation = "artifacthub.io/operatorCapabilities"
	prereleaseAnnotation           = "artifacthub.io/prerelease"
	recommendationsAnnotation      = "artifacthub.io/recommendations"
	securityUpdatesAnnotation      = "artifacthub.io/containsSecurityUpdates"
	signKeyAnnotation              = "artifacthub.io/signKey"

	helmChartConfigMediaType       = "application/vnd.cncf.helm.config.v1+json"
	helmChartContentLayerMediaType = "application/tar+gzip"
)

var (
	// containersImagesRE is a regexp used to extract containers images from
	// kubernetes manifests files.
	containersImagesRE = regexp.MustCompile(`\simage:\s(\S+)`)

	// errInvalidAnnotation indicates that the annotation provided is not valid.
	errInvalidAnnotation = errors.New("invalid annotation")

	// validOperatorCapabilities represents the valid operator capabilities
	// values that can be provided.
	validOperatorCapabilities = []string{
		"basic install",
		"seamless upgrades",
		"full lifecycle",
		"deep insights",
		"auto pilot",
	}
)

// TrackerSource is a hub.TrackerSource implementation for Helm repositories.
type TrackerSource struct {
	i  *hub.TrackerSourceInput
	il hub.HelmIndexLoader
	tg hub.OCITagsGetter
}

// NewTrackerSource creates a new TrackerSource instance.
func NewTrackerSource(i *hub.TrackerSourceInput, opts ...func(s *TrackerSource)) *TrackerSource {
	s := &TrackerSource{i: i}
	for _, o := range opts {
		o(s)
	}
	if s.il == nil {
		s.il = &repo.HelmIndexLoader{}
	}
	if s.tg == nil {
		s.tg = &repo.OCITagsGetter{}
	}
	return s
}

// GetPackagesAvailable implements the TrackerSource interface.
func (s *TrackerSource) GetPackagesAvailable() (map[string]*hub.Package, error) {
	var mu sync.Mutex
	packagesAvailable := make(map[string]*hub.Package)

	// Iterate over charts versions available in the repository
	charts, err := s.getCharts()
	if err != nil {
		return nil, err
	}
	limiter := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, chartVersions := range charts {
		for _, chartVersion := range chartVersions {
			// Return ASAP if context is cancelled
			select {
			case <-s.i.Svc.Ctx.Done():
				wg.Wait()
				return nil, s.i.Svc.Ctx.Err()
			default:
			}

			// Prepare and store package version
			limiter <- struct{}{}
			wg.Add(1)
			go func(chartVersion *helmrepo.ChartVersion) {
				defer func() {
					<-limiter
					wg.Done()
				}()
				p, err := s.preparePackage(chartVersion)
				if err != nil {
					s.warn(chartVersion.Metadata, fmt.Errorf("error preparing package: %w", err))
					return
				}
				mu.Lock()
				packagesAvailable[pkg.BuildKey(p)] = p
				mu.Unlock()
			}(chartVersion)
		}
	}
	wg.Wait()

	return packagesAvailable, nil
}

// getCharts returns the charts available in the repository.
func (s *TrackerSource) getCharts() (map[string][]*helmrepo.ChartVersion, error) {
	charts := make(map[string][]*helmrepo.ChartVersion)

	u, _ := url.Parse(s.i.Repository.URL)
	switch u.Scheme {
	case "http", "https":
		// Load repository index file
		indexFile, _, err := s.il.LoadIndex(s.i.Repository)
		if err != nil {
			return nil, fmt.Errorf("error loading repository index file: %w", err)
		}

		// Read available charts versions from index file
		for name, chartVersions := range indexFile.Entries {
			for _, chartVersion := range chartVersions {
				charts[name] = append(charts[name], chartVersion)
			}
		}
	case "oci":
		// Get versions (tags) available in the repository
		versions, err := s.tg.Tags(s.i.Svc.Ctx, s.i.Repository)
		if err != nil {
			return nil, fmt.Errorf("error getting repository available versions: %w", err)
		}

		// Prepare chart versions using the list of versions available
		name := path.Base(s.i.Repository.URL)
		for _, version := range versions {
			charts[name] = append(charts[name], &helmrepo.ChartVersion{
				Metadata: &chart.Metadata{
					Name:    name,
					Version: version,
				},
				URLs: []string{s.i.Repository.URL + ":" + version},
			})
		}
	default:
		return nil, repo.ErrSchemeNotSupported
	}

	return charts, nil
}

// preparePackage prepares a package version using the chart version provided.
func (s *TrackerSource) preparePackage(chartVersion *helmrepo.ChartVersion) (*hub.Package, error) {
	// Parse package version
	md := chartVersion.Metadata
	sv, err := semver.NewVersion(md.Version)
	if err != nil {
		return nil, fmt.Errorf("invalid package version: %w", err)
	}
	version := sv.String()

	// Prepare chart archive url
	if len(chartVersion.URLs) == 0 {
		return nil, errors.New("chart version does not contain any url")
	}
	chartURL, err := url.Parse(chartVersion.URLs[0])
	if err != nil {
		return nil, fmt.Errorf("invalid chart url %s: %w", chartVersion.URLs[0], err)
	}
	if !chartURL.IsAbs() {
		repoURL, _ := url.Parse(s.i.Repository.URL)
		chartURL.Scheme = repoURL.Scheme
		chartURL.Host = repoURL.Host
		if !strings.HasPrefix(chartURL.Path, "/") {
			chartURL.Path = path.Join(repoURL.Path, chartURL.Path)
		}
	}

	// Prepare package version
	p := &hub.Package{
		Name:       chartVersion.Name,
		Version:    version,
		Digest:     chartVersion.Digest,
		ContentURL: chartURL.String(),
		Repository: s.i.Repository,
	}
	if !chartVersion.Created.IsZero() {
		p.TS = chartVersion.Created.Unix()
	}

	// If the package version is not registered yet or if it needs to be
	// registered again, we need to enrich the package with extra information
	// available in the chart archive, like the readme file, the license, etc.
	// Otherwise, the minimal version of the package prepared above is enough.
	bypassDigestCheck := s.i.Svc.Cfg.GetBool("tracker.bypassDigestCheck")
	digest, ok := s.i.PackagesRegistered[pkg.BuildKey(p)]
	if !ok || chartVersion.Digest != digest || bypassDigestCheck {
		// Load chart from remote archive
		chrt, err := LoadChartArchive(
			s.i.Svc.Ctx,
			chartURL,
			&LoadChartArchiveOptions{
				HC:          s.i.Svc.Hc,
				GithubToken: s.i.Svc.Cfg.GetString("creds.githubToken"),
				GithubRL:    s.i.Svc.GithubRL,
				Username:    s.i.Repository.AuthUser,
				Password:    s.i.Repository.AuthPass,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("error loading chart (%s): %w", chartURL.String(), err)
		}
		md := chrt.Metadata

		// Validate chart version metadata for known issues and sanitize some strings
		if err := chrt.Validate(); err != nil {
			return nil, fmt.Errorf("invalid metadata: %w", err)
		}

		// Store logo when available if requested
		if md.Icon != "" {
			logoImageID, err := s.i.Svc.Is.DownloadAndSaveImage(s.i.Svc.Ctx, md.Icon)
			if err == nil {
				p.LogoURL = md.Icon
				p.LogoImageID = logoImageID
			} else {
				s.warn(md, fmt.Errorf("error getting logo image %s: %w", md.Icon, err))
			}
		}

		// Check if the chart version is signed (has provenance file)
		if repo.SchemeIsHTTP(chartURL) {
			hasProvenanceFile, err := s.chartHasProvenanceFile(chartURL.String())
			if err != nil {
				s.warn(md, fmt.Errorf("error checking provenance file: %w", err))
			}
			if hasProvenanceFile {
				p.Signed = hasProvenanceFile
			}
		}

		// Enrich package with data available in chart archive
		EnrichPackageFromChart(p, chrt)

		// Enrich package with information from annotations
		if err := EnrichPackageFromAnnotations(p, chrt.Metadata.Annotations); err != nil {
			return nil, fmt.Errorf("error enriching package from annotations: %w", err)
		}
	}

	return p, nil
}

// chartHasProvenanceFile checks if a chart version has a provenance file
// checking if a .prov file exists for the chart version url provided.
func (s *TrackerSource) chartHasProvenanceFile(u string) (bool, error) {
	req, _ := http.NewRequest("GET", u+".prov", nil)
	if s.i.Repository.AuthUser != "" || s.i.Repository.AuthPass != "" {
		req.SetBasicAuth(s.i.Repository.AuthUser, s.i.Repository.AuthPass)
	}
	resp, err := s.i.Svc.Hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("error reading provenance file: %w", err)
	}
	if !bytes.Contains(data, []byte("PGP SIGNATURE")) {
		return false, errors.New("invalid provenance file")
	}
	return true, nil
}

// warn is a helper that sends the error provided to the errors collector and
// logs it as a warning.
func (s *TrackerSource) warn(md *chart.Metadata, err error) {
	err = fmt.Errorf("%w (package: %s version: %s)", err, md.Name, md.Version)
	s.i.Svc.Logger.Warn().Err(err).Send()
	if !md.Deprecated {
		s.i.Svc.Ec.Append(s.i.Repository.RepositoryID, err.Error())
	}
}

// LoadChartArchiveOptions represents some options that can be provided to load
// a chart archive from its remote location.
type LoadChartArchiveOptions struct {
	HC          hub.HTTPClient
	Username    string
	Password    string
	GithubToken string
	GithubRL    *rate.Limiter
}

// LoadChartArchive loads a chart from a remote archive located at the url
// provided.
func LoadChartArchive(ctx context.Context, u *url.URL, o *LoadChartArchiveOptions) (*chart.Chart, error) {
	var r io.Reader

	switch u.Scheme {
	case "http", "https":
		// Get chart content
		req, _ := http.NewRequest("GET", u.String(), nil)
		req = req.WithContext(ctx)
		req.Header.Set("Accept-Encoding", "*")
		if u.Host == "github.com" || u.Host == "raw.githubusercontent.com" {
			// Authenticate and rate limit requests to Github
			if o.GithubToken != "" {
				req.Header.Set("Authorization", fmt.Sprintf("token %s", o.GithubToken))
			}
			if o.GithubRL != nil {
				_ = o.GithubRL.Wait(ctx)
			}
		}
		if o.Username != "" || o.Password != "" {
			req.SetBasicAuth(o.Username, o.Password)
		}
		hc := o.HC
		if hc == nil {
			hc = util.SetupHTTPClient(false)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status code received: %d", resp.StatusCode)
		}
		r = resp.Body
	case "oci":
		// Pull reference layers from OCI registry
		ref := strings.TrimPrefix(u.String(), hub.RepositoryOCIPrefix)
		resolverOptions := docker.ResolverOptions{}
		if o.Username != "" || o.Password != "" {
			resolverOptions.Authorizer = docker.NewDockerAuthorizer(
				docker.WithAuthCreds(func(string) (string, string, error) {
					return o.Username, o.Password, nil
				}),
			)
		}
		store := content.NewMemoryStore()
		_, layers, err := oras.Pull(
			ctxo.WithLoggerDiscarded(ctx),
			docker.NewResolver(resolverOptions),
			ref,
			store,
			oras.WithPullEmptyNameAllowed(),
			oras.WithAllowedMediaTypes([]string{helmChartConfigMediaType, helmChartContentLayerMediaType}),
		)
		if err != nil {
			return nil, err
		}

		// Create reader for Helm chart content layer, if available
		for _, layer := range layers {
			if layer.MediaType == helmChartContentLayerMediaType {
				_, b, ok := store.Get(layer)
				if ok {
					r = bytes.NewReader(b)
					break
				}
			}
		}
		if r == nil {
			return nil, errors.New("content layer not found")
		}
	default:
		return nil, repo.ErrSchemeNotSupported
	}

	// Load chart from reader previously set up
	chrt, err := loader.LoadArchive(r)
	if err != nil {
		return nil, err
	}
	return chrt, nil
}

// EnrichPackageFromChart adds some extra information to the package from the
// chart archive.
func EnrichPackageFromChart(p *hub.Package, chrt *chart.Chart) {
	md := chrt.Metadata
	p.Description = md.Description
	p.Keywords = md.Keywords
	p.HomeURL = md.Home
	p.AppVersion = md.AppVersion
	p.Deprecated = md.Deprecated
	p.ValuesSchema = chrt.Schema
	p.Data = map[string]interface{}{}

	// API version
	p.Data["apiVersion"] = chrt.Metadata.APIVersion

	// Containers images
	imagesRefs, err := extractContainersImages(chrt)
	if err == nil && len(imagesRefs) > 0 {
		containersImages := make([]*hub.ContainerImage, 0, len(imagesRefs))
		for _, imageRef := range imagesRefs {
			containersImages = append(containersImages, &hub.ContainerImage{Image: imageRef})
		}
		if err := pkg.ValidateContainersImages(containersImages); err == nil {
			p.ContainersImages = containersImages
		}
	}

	// Dependencies
	dependencies := make([]map[string]string, 0, len(md.Dependencies))
	for _, dependency := range md.Dependencies {
		dependencies = append(dependencies, map[string]string{
			"name":       dependency.Name,
			"version":    dependency.Version,
			"repository": dependency.Repository,
		})
	}
	if len(dependencies) > 0 {
		p.Data["dependencies"] = dependencies
	}

	// Kubernetes version
	p.Data["kubeVersion"] = chrt.Metadata.KubeVersion

	// License
	licenseFile := getFile(chrt, "LICENSE")
	if licenseFile != nil {
		p.License = license.Detect(licenseFile.Data)
	}

	// Links
	links := make([]*hub.Link, 0, len(md.Sources))
	for _, sourceURL := range md.Sources {
		links = append(links, &hub.Link{
			Name: "source",
			URL:  sourceURL,
		})
	}
	if len(links) > 0 {
		p.Links = links
	}

	// Maintainers
	var maintainers []*hub.Maintainer
	for _, entry := range md.Maintainers {
		if entry.Email != "" {
			maintainers = append(maintainers, &hub.Maintainer{
				Name:  entry.Name,
				Email: entry.Email,
			})
		}
	}
	if len(maintainers) > 0 {
		p.Maintainers = maintainers
	}

	// Operator
	if strings.Contains(strings.ToLower(md.Name), "operator") {
		p.IsOperator = true
	}

	// Readme
	readme := getFile(chrt, "README.md")
	if readme != nil {
		p.Readme = string(readme.Data)
	}

	// Type
	p.Data["type"] = chrt.Metadata.Type
}

// extractContainersImages extracts the containers images references found in
// the manifest generated as a result of Helm dry-run install with the default
// values.
func extractContainersImages(chrt *chart.Chart) ([]string, error) {
	// Dry-run Helm install
	install := action.NewInstall(&action.Configuration{
		Log: func(string, ...interface{}) {},
	})
	install.ReleaseName = "release-name"
	install.DryRun = true
	install.DisableHooks = true
	install.Replace = true
	install.ClientOnly = true
	install.IncludeCRDs = true
	install.DependencyUpdate = false
	release, err := install.Run(chrt, chartutil.Values{})
	if err != nil {
		return nil, err
	}

	// Extract containers images from release manifest
	results := containersImagesRE.FindAllStringSubmatch(release.Manifest, -1)
	images := make([]string, 0, len(results))
	for _, result := range results {
		image := strings.Trim(result[1], `"'`)
		if image != "" && !contains(images, image) {
			images = append(images, image)
		}
	}

	return images, nil
}

// EnrichPackageFromAnnotations adds some extra information to the package from
// the provided annotations.
func EnrichPackageFromAnnotations(p *hub.Package, annotations map[string]string) error {
	var result *multierror.Error

	// Changes
	if v, ok := annotations[changesAnnotation]; ok {
		changes, err := source.ParseChangesAnnotation(v)
		if err != nil {
			result = multierror.Append(result, err)
		} else {
			p.Changes = changes
		}
	}

	// CRDs
	if v, ok := annotations[crdsAnnotation]; ok {
		var crds []interface{}
		if err := yaml.Unmarshal([]byte(v), &crds); err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid crds value", errInvalidAnnotation))
		} else {
			p.CRDs = crds
		}
	}

	// CRDs examples
	if v, ok := annotations[crdsExamplesAnnotation]; ok {
		var crdsExamples []interface{}
		if err := yaml.Unmarshal([]byte(v), &crdsExamples); err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid crdsExamples value", errInvalidAnnotation))
		} else {
			p.CRDsExamples = crdsExamples
		}
	}

	// Images
	if v, ok := annotations[imagesAnnotation]; ok {
		var images []*hub.ContainerImage
		if err := yaml.Unmarshal([]byte(v), &images); err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid images value", errInvalidAnnotation))
		} else {
			if err := pkg.ValidateContainersImages(images); err != nil {
				result = multierror.Append(result, fmt.Errorf("%w: %s", errInvalidAnnotation, err.Error()))
			} else {
				p.ContainersImages = images
			}
		}
	}

	// License
	if v, ok := annotations[licenseAnnotation]; ok && v != "" {
		p.License = v
	}

	// Links
	if v, ok := annotations[linksAnnotation]; ok {
		var links []*hub.Link
		if err := yaml.Unmarshal([]byte(v), &links); err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid links value", errInvalidAnnotation))
		} else {
		LL:
			for _, link := range links {
				for _, pLink := range p.Links {
					if link.URL == pLink.URL {
						pLink.Name = link.Name
						continue LL
					}
				}
				p.Links = append(p.Links, link)
			}
		}
	}

	// Maintainers
	if v, ok := annotations[maintainersAnnotation]; ok {
		var maintainers []*hub.Maintainer
		if err := yaml.Unmarshal([]byte(v), &maintainers); err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid maintainers value", errInvalidAnnotation))
		} else {
		ML:
			for _, maintainer := range maintainers {
				for _, pMaintainer := range p.Maintainers {
					if maintainer.Email == pMaintainer.Email {
						pMaintainer.Name = maintainer.Name
						continue ML
					}
				}
				p.Maintainers = append(p.Maintainers, maintainer)
			}
		}
	}

	// Operator flag
	if v, ok := annotations[operatorAnnotation]; ok {
		isOperator, err := strconv.ParseBool(v)
		if err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid operator value", errInvalidAnnotation))
		} else {
			p.IsOperator = isOperator
		}
	}

	// Operator capabilities
	if v, ok := annotations[operatorCapabilitiesAnnotation]; ok {
		v = strings.ToLower(v)
		if !contains(validOperatorCapabilities, v) {
			result = multierror.Append(result, fmt.Errorf("%w: invalid operator capabilities value", errInvalidAnnotation))
		} else {
			p.Capabilities = v
		}
	}

	// Prerelease
	if v, ok := annotations[prereleaseAnnotation]; ok {
		prerelease, err := strconv.ParseBool(v)
		if err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid prerelease value", errInvalidAnnotation))
		} else {
			p.Prerelease = prerelease
		}
	}

	// Recommendations
	if v, ok := annotations[recommendationsAnnotation]; ok {
		var recommendations []*hub.Recommendation
		if err := yaml.Unmarshal([]byte(v), &recommendations); err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid recommendations value", errInvalidAnnotation))
		} else {
			p.Recommendations = recommendations
		}
	}

	// Security updates
	if v, ok := annotations[securityUpdatesAnnotation]; ok {
		containsSecurityUpdates, err := strconv.ParseBool(v)
		if err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid containsSecurityUpdates value", errInvalidAnnotation))
		} else {
			p.ContainsSecurityUpdates = containsSecurityUpdates
		}
	}

	// Sign key
	if v, ok := annotations[signKeyAnnotation]; ok {
		var signKey *hub.SignKey
		if err := yaml.Unmarshal([]byte(v), &signKey); err != nil {
			result = multierror.Append(result, fmt.Errorf("%w: invalid sign key value", errInvalidAnnotation))
		} else {
			if signKey.URL == "" {
				result = multierror.Append(result, fmt.Errorf("%w: sign key url not provided", errInvalidAnnotation))
			} else {
				p.SignKey = signKey
			}
		}
	}

	return result.ErrorOrNil()
}

// getFile returns the file requested from the provided chart.
func getFile(chrt *chart.Chart, name string) *chart.File {
	for _, file := range chrt.Files {
		if file.Name == name {
			return file
		}
	}
	return nil
}

// contains is a helper to check if a list contains the string provided.
func contains(l []string, e string) bool {
	for _, x := range l {
		if x == e {
			return true
		}
	}
	return false
}
