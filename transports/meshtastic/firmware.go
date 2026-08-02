// Fetching Meshtastic firmware (RB-2). Everything here is about ONE
// principle: the flashing recipe is data published by the firmware release,
// never a constant remembered by us.
//
// A Meshtastic release publishes three things we use, and each one replaces
// something we would otherwise have to hardcode and would eventually get
// wrong:
//
//	firmware-<version>.json         every board variant, with its MCU family
//	<variant>.mt.json  (in the zip) the files to write and the partition
//	                                table that says where each one goes
//	md5 per file       (in the same) what to verify before writing anything
//
// So we never carry a board list, never carry a flash offset, and never write
// a byte we have not checksummed. When Meshtastic adds a board or moves a
// partition, this code needs no edit — and, more to the point, cannot quietly
// go on believing the old answer.
//
// The per-platform zips are large (the ESP32-S3 one is ~170 MB) and hold
// every variant for that MCU. We do not download it. `archive/zip` reads
// through an io.ReaderAt, and HTTP range requests make one out of a URL, so
// we pull the central directory and then only the handful of files the chosen
// board actually needs — a few megabytes instead of a hundred and seventy.
package meshtastic

import (
	"archive/zip"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FirmwareRelease is one published Meshtastic release.
type FirmwareRelease struct {
	Version string           // e.g. "2.7.26.54e0d8d"
	Tag     string           // the git tag the assets hang off
	Targets []FirmwareTarget // every board variant in this release
}

// FirmwareTarget is one board variant as the RELEASE names it. Board is the
// build target ("heltec-v3"); MCU is the chip family ("esp32s3"), which is
// the only field a boot-ROM banner can be compared against.
type FirmwareTarget struct {
	Board string `json:"board"`
	MCU   string `json:"platform"`
}

// FirmwareFile is one file to write, and where.
type FirmwareFile struct {
	Name   string
	MD5    string
	Bytes  int64
	Offset uint32 // resolved from the release's own partition table
}

// FlashPlan is everything that will be written to a device, in order, with
// the checksums each file must match. Built before anything is downloaded so
// it can be shown to a person and refused for free.
type FlashPlan struct {
	Board   string
	MCU     string
	Version string
	Files   []FirmwareFile
	ZipURL  string
}

// TotalBytes is what will actually be downloaded and written.
func (p FlashPlan) TotalBytes() int64 {
	var n int64
	for _, f := range p.Files {
		n += f.Bytes
	}
	return n
}

const releaseAPI = "https://api.github.com/repos/meshtastic/firmware/releases/latest"

// LatestRelease asks GitHub for the newest published firmware and reads the
// variant list out of it.
func LatestRelease(client *http.Client) (FirmwareRelease, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Get(releaseAPI)
	if err != nil {
		return FirmwareRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return FirmwareRelease{}, fmt.Errorf("meshtastic release api: %s", resp.Status)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return FirmwareRelease{}, err
	}
	// The variant manifest is the asset named firmware-<version>.json — the
	// only .json among them.
	var manifestURL string
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, "firmware-") && strings.HasSuffix(a.Name, ".json") {
			manifestURL = a.URL
			break
		}
	}
	if manifestURL == "" {
		return FirmwareRelease{}, errors.New("meshtastic release carries no variant manifest")
	}
	mr, err := client.Get(manifestURL)
	if err != nil {
		return FirmwareRelease{}, err
	}
	defer mr.Body.Close()
	var man struct {
		Version string           `json:"version"`
		Targets []FirmwareTarget `json:"targets"`
	}
	if err := json.NewDecoder(io.LimitReader(mr.Body, 4<<20)).Decode(&man); err != nil {
		return FirmwareRelease{}, err
	}
	out := FirmwareRelease{Version: man.Version, Tag: rel.Tag, Targets: man.Targets}
	sort.Slice(out.Targets, func(i, j int) bool { return out.Targets[i].Board < out.Targets[j].Board })
	return out, nil
}

// TargetsForChip narrows the variant list to one MCU family.
//
// This NARROWS; it does not choose. A boot-ROM banner identifies the chip and
// nothing else, and several dozen different boards share one chip. Returning
// a single "best" match here would be the exact confident lie this package
// refuses everywhere else.
func (r FirmwareRelease) TargetsForChip(chip string) []FirmwareTarget {
	chip = strings.ToLower(strings.TrimSpace(chip))
	if chip == "" {
		return r.Targets // nothing known: narrowing would be inventing
	}
	var out []FirmwareTarget
	for _, t := range r.Targets {
		if strings.EqualFold(t.MCU, chip) {
			out = append(out, t)
		}
	}
	return out
}

// ZipURLFor is the per-MCU archive holding every variant for that chip.
func (r FirmwareRelease) ZipURLFor(mcu string) string {
	return fmt.Sprintf(
		"https://github.com/meshtastic/firmware/releases/download/%s/firmware-%s-%s.zip",
		r.Tag, mcu, r.Version)
}

// PlanFor reads the chosen variant's own install manifest out of the release
// archive and turns it into a plan. Nothing is downloaded but the manifest.
func (r FirmwareRelease) PlanFor(client *http.Client, board, mcu string) (FlashPlan, error) {
	url := r.ZipURLFor(mcu)
	zr, err := openRemoteZip(client, url)
	if err != nil {
		return FlashPlan{}, err
	}
	want := fmt.Sprintf("firmware-%s-%s.mt.json", board, r.Version)
	var raw []byte
	for _, f := range zr.File {
		if f.Name != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return FlashPlan{}, err
		}
		raw, err = io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		if err != nil {
			return FlashPlan{}, err
		}
		break
	}
	if raw == nil {
		return FlashPlan{}, fmt.Errorf("release %s has no install manifest for board %q", r.Version, board)
	}
	plan, err := ParseInstallManifest(raw)
	if err != nil {
		return FlashPlan{}, err
	}
	plan.ZipURL = url
	return plan, nil
}

// installManifest mirrors the <variant>.mt.json shipped inside the archive.
type installManifest struct {
	Version   string `json:"version"`
	Target    string `json:"platformioTarget"`
	MCU       string `json:"mcu"`
	FileEntry []struct {
		Name  string `json:"name"`
		MD5   string `json:"md5"`
		Bytes int64  `json:"bytes"`
		Part  string `json:"part_name"`
	} `json:"files"`
	Part []struct {
		Name   string `json:"name"`
		Offset string `json:"offset"`
	} `json:"part"`
}

// ParseInstallManifest turns a variant's published manifest into a plan.
//
// Only two kinds of file are written to a fresh device, and the rule for each
// comes from the manifest itself rather than from us:
//
//   - the FACTORY image carries bootloader, partition table and application
//     in one blob and goes at offset 0. It is the only file with no
//     part_name, which is how it identifies itself.
//   - a file WITH a part_name goes at the offset the partition table gives
//     for that partition.
//
// Everything else in the manifest (the .elf, the OTA slot) is not part of a
// first install and is left out — named here so its absence is a decision
// rather than an oversight.
func ParseInstallManifest(raw []byte) (FlashPlan, error) {
	var m installManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return FlashPlan{}, err
	}
	offsets := map[string]uint32{}
	for _, p := range m.Part {
		v, err := strconv.ParseUint(strings.TrimPrefix(p.Offset, "0x"), 16, 32)
		if err != nil {
			continue
		}
		offsets[p.Name] = uint32(v)
	}
	plan := FlashPlan{Board: m.Target, MCU: m.MCU, Version: m.Version}
	for _, f := range m.FileEntry {
		switch {
		case strings.HasSuffix(f.Name, ".factory.bin"):
			plan.Files = append(plan.Files, FirmwareFile{
				Name: f.Name, MD5: f.MD5, Bytes: f.Bytes, Offset: 0})
		case f.Part == "spiffs":
			off, ok := offsets[f.Part]
			if !ok {
				return FlashPlan{}, fmt.Errorf(
					"manifest names partition %q for %s but the partition table does not describe it",
					f.Part, f.Name)
			}
			plan.Files = append(plan.Files, FirmwareFile{
				Name: f.Name, MD5: f.MD5, Bytes: f.Bytes, Offset: off})
		}
	}
	if len(plan.Files) == 0 {
		return FlashPlan{}, errors.New("install manifest lists nothing to write")
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Offset < plan.Files[j].Offset })
	return plan, nil
}

// Download pulls exactly the planned files out of the remote archive and
// writes them through `save`, verifying each against the MD5 the release
// published. A file that does not match is never handed on.
func (p FlashPlan) Download(client *http.Client, save func(name string, data []byte) error) error {
	zr, err := openRemoteZip(client, p.ZipURL)
	if err != nil {
		return err
	}
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	for _, want := range p.Files {
		f, ok := byName[want.Name]
		if !ok {
			return fmt.Errorf("archive does not contain %s", want.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(rc, want.Bytes+1))
		rc.Close()
		if err != nil {
			return err
		}
		sum := md5.Sum(data)
		if got := hex.EncodeToString(sum[:]); got != want.MD5 {
			return fmt.Errorf("%s failed its checksum: release says %s, downloaded bytes are %s "+
				"— nothing was written to the device", want.Name, want.MD5, got)
		}
		if err := save(want.Name, data); err != nil {
			return err
		}
	}
	return nil
}

// ---- HTTP range-backed io.ReaderAt ----

type httpReaderAt struct {
	client *http.Client
	url    string
	size   int64
}

func (h *httpReaderAt) ReadAt(p []byte, off int64) (int, error) {
	req, err := http.NewRequest(http.MethodGet, h.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("range request refused (%s) — this needs a server that "+
			"supports partial downloads", resp.Status)
	}
	return io.ReadFull(resp.Body, p)
}

func openRemoteZip(client *http.Client, url string) (*zip.Reader, error) {
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("firmware archive: %s", resp.Status)
	}
	if resp.ContentLength <= 0 {
		return nil, errors.New("firmware archive did not report its size")
	}
	return zip.NewReader(&httpReaderAt{client: client, url: url, size: resp.ContentLength},
		resp.ContentLength)
}
