// Package backup provides offline, hash-verified backups of application state,
// encryption keys and document blobs. It never replaces an existing data dir.
package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/sflow"
)

const MaxArchiveBytes int64 = 8 << 30
const maxStateBytes int64 = 128 << 20
const maxManifestBytes int64 = 32 << 20
const maxArchiveFiles = 65536

var devicePath = regexp.MustCompile(`^devices/(discovery-[a-f0-9]{32}|observation-[a-f0-9]{64})\.json$`)
var integrationPath = regexp.MustCompile(`^integrations/snapshots/[A-Za-z0-9_-]{1,100}\.json$`)
var policyPath = regexp.MustCompile(`^policies/[A-Za-z0-9_-]{1,100}\.json$`)
var deploymentPath = regexp.MustCompile(`^deployments/[A-Za-z0-9_-]{1,100}\.enc$`)
var provisioningPath = regexp.MustCompile(`^provisioning/[A-Za-z0-9_-]{1,100}\.enc$`)
var diagnosticPath = regexp.MustCompile(`^(atlas|looking-glass)/[A-Za-z0-9_-]{1,100}\.enc$`)
var trafficPath = regexp.MustCompile(`^telemetry/archive/((5m|1h|1d)-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9]{2}|catalog)-[a-f0-9]{64}\.json\.gz$`)
var legacyTrafficPath = regexp.MustCompile(`^legacy-traffic/batches/[a-f0-9]{64}\.json\.gz$`)

var blobPath = regexp.MustCompile(`^blobs/blob-[A-Za-z0-9_-]{24}$`)

type Entry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	Files     []Entry   `json:"files"`
}

func allowed(name string) bool {
	return name == "state.json" || name == "security.key" || blobPath.MatchString(name) || policyPath.MatchString(name) || deploymentPath.MatchString(name) || provisioningPath.MatchString(name) || diagnosticPath.MatchString(name) || name == "telemetry/snapshot.json" || name == "telemetry/journal.bin" || trafficPath.MatchString(name) || name == "sflow/flows.snapshot.json" || name == "sflow/flows.wal" || name == "legacy-traffic/legacy-manifest.json" || legacyTrafficPath.MatchString(name) || devicePath.MatchString(name) || integrationPath.MatchString(name)
}
func limitFor(name string) int64 {
	if name == "security.key" {
		return 32
	}
	if trafficPath.MatchString(name) {
		return 16 << 20
	}
	if name == "legacy-traffic/legacy-manifest.json" {
		return 8 << 20
	}
	if legacyTrafficPath.MatchString(name) {
		return 16 << 20
	}
	if name == "telemetry/journal.bin" {
		return 256 << 20
	}
	if name == "sflow/flows.snapshot.json" || name == "sflow/flows.wal" {
		return sflow.MaxStoreBytes
	}
	if devicePath.MatchString(name) || integrationPath.MatchString(name) {
		return 16 << 20
	}
	if deploymentPath.MatchString(name) || provisioningPath.MatchString(name) || diagnosticPath.MatchString(name) {
		return 24 << 20
	}
	if name == "state.json" || policyPath.MatchString(name) || strings.HasPrefix(name, "telemetry/") {
		return maxStateBytes
	}
	return 25 << 20
}

// Create refuses a running instance and refuses overwriting a backup. The
// archive contains credentials; the published file has mode 0600.
func Create(dataDir, output string) error {
	if st, err := os.Lstat(filepath.Join(dataDir, "state.json")); err != nil || !st.Mode().IsRegular() {
		return errors.New("backup requires an existing regular state.json")
	}
	store, err := app.OpenStore(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	// Convert legacy inline configurations while holding the offline lock, and
	// validate encrypted artifacts before selecting files for the archive.
	if err := app.MigrateDeploymentArtifacts(store); err != nil {
		return fmt.Errorf("deployment backup preflight failed: %w", err)
	}
	if err := app.ValidateProvisioningArtifacts(store); err != nil {
		return fmt.Errorf("provisioning backup preflight failed: %w", err)
	}
	if err := app.ValidateMemberLogoArtifacts(store); err != nil {
		return fmt.Errorf("member logo backup preflight failed: %w", err)
	}
	if err := app.ValidateLookingGlassArtifacts(store); err != nil {
		return fmt.Errorf("diagnostic backup preflight failed: %w", err)
	}
	state := store.Snapshot()
	key, err := os.ReadFile(filepath.Join(dataDir, "security.key"))
	if err != nil {
		return err
	}
	if err := app.ValidateAtlasArtifacts(dataDir, state, key); err != nil {
		return fmt.Errorf("Atlas backup preflight failed: %w", err)
	}
	files := []string{"state.json", "security.key"}
	for _, id := range app.AtlasArtifactIDs(state) {
		files = append(files, "atlas/"+id+".enc")
	}
	for _, id := range app.LookingGlassArtifactIDs(state) {
		files = append(files, "looking-glass/"+id+".enc")
	}
	for _, d := range state.Deployments {
		if !d.Pruned {
			files = append(files, "deployments/"+d.ID+".enc")
		}
	}
	for _, d := range state.ProvisionRevisions {
		if !d.Pruned {
			files = append(files, "provisioning/"+d.ID+".enc")
		}
	}
	for _, d := range state.Documents {
		files = append(files, "blobs/"+d.BlobID)
		for _, v := range d.Revisions {
			files = append(files, "blobs/"+v.BlobID)
		}
	}
	for _, logo := range state.MemberLogos {
		if logo.BlobID != "" {
			files = append(files, "blobs/"+logo.BlobID)
		}
	}
	for _, snapshot := range state.PolicySnapshots {
		files = append(files, "policies/"+snapshot.ID+".json")
	}
	trafficFiles, err := observationFiles(dataDir)
	if err != nil {
		return err
	}
	files = append(files, trafficFiles...)
	for _, letter := range state.LOAs {
		files = append(files, "blobs/"+letter.PDFBlobID, "blobs/"+letter.HTMLBlobID)
	}
	for _, p := range state.ExternalProposals {
		if p.SnapshotPruned {
			continue
		}
		files = append(files, "integrations/snapshots/"+p.ID+".json")
	}
	for _, d := range state.DeviceDiscoveries {
		if d.PrunedAt != nil {
			continue
		}
		files = append(files, "devices/"+d.SnapshotFile)
	}
	if entries, e := os.ReadDir(filepath.Join(dataDir, "devices")); e == nil {
		count := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "observation-") && devicePath.MatchString("devices/"+entry.Name()) {
				count++
				if count > 4096 {
					return errors.New("too many observation files")
				}
				files = append(files, "devices/"+entry.Name())
			}
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	sort.Strings(files)
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	out, err := os.CreateTemp(filepath.Dir(output), ".ixp-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	manifest := Manifest{Version: 1, CreatedAt: time.Now().UTC(), Files: []Entry{}}
	if len(files) > maxArchiveFiles {
		return errors.New("backup exceeds65536 files")
	}
	var total int64
	for _, name := range files {
		if !allowed(name) {
			return errors.New("invalid referenced backup path")
		}
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > limitFor(name) {
			f.Close()
			return fmt.Errorf("invalid backup file %s", name)
		}
		total += info.Size()
		if total > MaxArchiveBytes {
			f.Close()
			return errors.New("backup exceeds 8 GiB supported limit")
		}
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}); err != nil {
			f.Close()
			return err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(tw, h), f)
		f.Close()
		if err != nil || n != info.Size() {
			return fmt.Errorf("backup file changed: %s", name)
		}
		manifest.Files = append(manifest.Files, Entry{name, n, hex.EncodeToString(h.Sum(nil))})
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if int64(len(b)) > maxManifestBytes {
		return errors.New("backup manifest exceeds32 MiB")
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err = tw.Write(b); err != nil {
		return err
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Link(out.Name(), output); err != nil {
		return fmt.Errorf("publish backup without overwriting: %w", err)
	}
	return syncDir(filepath.Dir(output))
}

// Restore stages and validates every file before publishing a new data dir.
// Absolute paths, links, unknown paths, duplicate entries and hash mismatches
// are rejected. Existing directories are never used as extraction targets.
func Restore(input, target string) error {
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return errors.New("restore target must not exist")
	}
	parent := filepath.Dir(target)
	stage, err := os.MkdirTemp(parent, ".ixp-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := prepareRestore(input, stage); err != nil {
		return err
	}
	// Claim the destination exclusively; rename replaces only the empty directory
	// just created by this operation, never a pre-existing application directory.
	if err = os.Mkdir(target, 0700); err != nil {
		return fmt.Errorf("claim restore destination: %w", err)
	}
	// POSIX rename may replace our own empty directory; os.Rename rejects
	// directory destinations before invoking the syscall.
	if err = syscall.Rename(stage, target); err != nil {
		_ = os.Remove(target)
		return err
	}
	return syncDir(parent)
}

// Verify checks whether this version can restore the archive. It uses exactly
// the restore validation path, including opening the staged application state.
// The work directory is only a parent for the private temporary copy.
func Verify(input, workDir string) error {
	stage, err := os.MkdirTemp(workDir, ".ixp-verify-*")
	if err != nil {
		return err
	}
	verifyErr := prepareRestore(input, stage)
	return errors.Join(verifyErr, os.RemoveAll(stage))
}

// prepareRestore extracts and validates an archive inside a fresh private
// directory. Its caller owns cleanup and any publication of the directory.
func prepareRestore(input, stage string) error {
	archive, err := os.Open(input)
	if err != nil {
		return err
	}
	defer archive.Close()
	buffered := bufio.NewReader(archive)
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return err
	}
	defer gz.Close()
	gz.Multistream(false)
	for _, dir := range []string{"blobs", "policies", "telemetry", "telemetry/archive", "sflow", "legacy-traffic", "legacy-traffic/batches", "devices", "deployments", "provisioning", "atlas", "looking-glass", "integrations", "integrations/snapshots"} {
		if err = os.Mkdir(filepath.Join(stage, dir), 0700); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(io.LimitReader(gz, MaxArchiveBytes+maxStateBytes))
	entries := map[string]Entry{}
	var manifest Manifest
	seenManifest := false
	var total int64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := header.Name
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return errors.New("backup must contain regular files only")
		}
		if name == "manifest.json" {
			if seenManifest || header.Size > maxManifestBytes || header.Size < 0 {
				return errors.New("invalid manifest")
			}
			seenManifest = true
			d := json.NewDecoder(io.LimitReader(tr, maxManifestBytes+1))
			d.DisallowUnknownFields()
			if err = d.Decode(&manifest); err != nil {
				return err
			}
			if d.Decode(new(any)) != io.EOF {
				return errors.New("invalid trailing manifest JSON")
			}
			continue
		}
		if !allowed(name) || header.Size < 0 || header.Size > limitFor(name) {
			return fmt.Errorf("unsafe or oversized backup path %q", name)
		}
		if _, exists := entries[name]; exists {
			return errors.New("duplicate backup file")
		}
		if len(entries) >= maxArchiveFiles {
			return errors.New("backup exceeds65536 files")
		}
		total += header.Size
		if total > MaxArchiveBytes {
			return errors.New("backup exceeds 8 GiB supported limit")
		}
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(f, h), tr)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err = errors.Join(copyErr, syncErr, closeErr); err != nil {
			return err
		}
		if n != header.Size {
			return errors.New("truncated backup file")
		}
		entries[name] = Entry{name, n, hex.EncodeToString(h.Sum(nil))}
	}
	// Ensure the gzip checksum is verified, and refuse hidden concatenated tar
	// content. Only the format emitted by Create is accepted.
	rest, err := io.ReadAll(io.LimitReader(gz, 1025))
	if err != nil || len(rest) != 0 {
		return errors.New("unexpected trailing or corrupt backup data")
	}
	if _, err := buffered.Peek(1); err != io.EOF {
		return errors.New("trailing compressed backup data")
	}
	if !seenManifest || manifest.Version != 1 || len(manifest.Files) != len(entries) {
		return errors.New("manifest version or file count mismatch")
	}
	checked := map[string]bool{}
	for _, entry := range manifest.Files {
		actual, ok := entries[entry.Name]
		if !ok || checked[entry.Name] || actual != entry {
			return errors.New("backup manifest checksum mismatch")
		}
		checked[entry.Name] = true
	}
	if !checked["state.json"] || !checked["security.key"] || entries["security.key"].Size != 32 {
		return errors.New("backup state or encryption key missing")
	}
	// Check exact immutable references before opening the application, because
	// opening may apply current retention and remove legitimately expired files.
	observationEntries, err := observationFiles(stage)
	if err != nil {
		return err
	}
	referencedObservations := map[string]bool{}
	for _, name := range observationEntries {
		referencedObservations[name] = true
	}
	for name := range entries {
		if (strings.HasPrefix(name, "telemetry/") || strings.HasPrefix(name, "sflow/") || strings.HasPrefix(name, "legacy-traffic/")) && !referencedObservations[name] {
			return errors.New("backup contains an unreferenced observation artifact")
		}
	}
	// New opens no listener. It validates state graph, security-key availability
	// and document references/sizes before any data dir becomes visible.
	verified, err := app.New(app.Config{DataDir: stage})
	if err != nil {
		return fmt.Errorf("restored state invalid: %w", err)
	}
	if err = verified.InvalidateRememberedDevicesAfterRestore(); err != nil {
		_ = verified.Close()
		return fmt.Errorf("invalidate restored browser grants: %w", err)
	}
	err = verified.Close()
	if err != nil {
		return err
	}
	// Reject unreferenced blob entries instead of silently discarding inputs.
	for name := range entries {
		if strings.HasPrefix(name, "blobs/") || strings.HasPrefix(name, "policies/") || strings.HasPrefix(name, "deployments/") || strings.HasPrefix(name, "provisioning/") || diagnosticPath.MatchString(name) {
			if _, err = os.Stat(filepath.Join(stage, name)); err != nil {
				return errors.New("backup contains an unreferenced artifact")
			}
		}
	}
	if err = syncDir(filepath.Join(stage, "blobs")); err != nil {
		return err
	}
	if err = syncDir(filepath.Join(stage, "deployments")); err != nil {
		return err
	}
	if err = syncDir(filepath.Join(stage, "provisioning")); err != nil {
		return err
	}
	for _, name := range []string{"atlas", "looking-glass", "telemetry/archive", "telemetry", "sflow", "legacy-traffic/batches", "legacy-traffic"} {
		if err := syncDir(filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	return syncDir(stage)
}
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
