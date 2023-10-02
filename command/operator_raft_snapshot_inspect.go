// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	iradix "github.com/hashicorp/go-immutable-radix"

	// protoio "github.com/gogo/protobuf/io"
	protoio "github.com/hashicorp/vault/physical/raft"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	"github.com/hashicorp/vault/sdk/plugin/pb"
	"github.com/mitchellh/cli"
	"github.com/posener/complete"
)

var (
	_ cli.Command             = (*OperatorRaftSnapshotInspectCommand)(nil)
	_ cli.CommandAutocomplete = (*OperatorRaftSnapshotInspectCommand)(nil)
)

type OperatorRaftSnapshotInspectCommand struct {
	*BaseCommand
	kvDetails bool
	kvDepth   int
	kvFilter  string
}

type MetadataInfo struct {
	ID      string
	Size    int64
	Index   uint64
	Term    uint64
	Version raft.SnapshotVersion
}

type typeStats struct {
	Name string
	// skipping sum for v1
	// Sum   int
	Count int
}

// // SnapshotInfo is used for passing snapshot stat
// // information between functions
type SnapshotInfo struct {
	Meta MetadataInfo
	// Note: we are not calculating these stats in v1
	// Stats       map[uint8]typeStats
	StatsKV      map[string]typeStats
	TotalSize    int
	TotalCountKV int
	// not supported in v1
	// TotalSizeKV int
}

// Temporarily removing this struct as we're not calculating size in v1
// countingReader helps keep track of the bytes we have read
// when reading snapshots
// type countingReader struct {
// 	wrappedReader io.Reader
// 	read          int
// }

// func (r *countingReader) Read(p []byte) (n int, err error) {
// 	n, err = r.wrappedReader.Read(p)
// 	if err == nil {
// 		r.read += n
// 	}
// 	return n, err
// }

func (c *OperatorRaftSnapshotInspectCommand) Synopsis() string {
	return "Inspects raft snapshot"
}

func (c *OperatorRaftSnapshotInspectCommand) Help() string {
	helpText := `
	Usage: vault operator raft snapshot inspect <snapshot_file>
	
	Inspects a snapshot file.
	
	$ vault operator raft snapshot inspect raft.snap
	
	`
	c.Flags().Help()

	return strings.TrimSpace(helpText)
}

// TODO: add following flags: kvdetails, kvdepth, kvfilter, format
func (c *OperatorRaftSnapshotInspectCommand) Flags() *FlagSets {
	set := c.flagSet(FlagSetHTTP | FlagSetOutputFormat)

	f := set.NewFlagSet("Command Options")

	f.BoolVar(&BoolVar{
		Name:    "kvdetails",
		Target:  &c.kvDetails,
		Default: false,
		Usage:   "Provides information about usage for KV data stored in Vault.",
	})

	f.IntVar(&IntVar{
		Name:    "kvdepth",
		Target:  &c.kvDepth,
		Default: 2,
		Usage:   "Can only be used with -kvdetails. The key prefix depth used to breakdown KV store data. Defaults to 2.",
	})

	f.StringVar(&StringVar{
		Name:    "kvfilter",
		Target:  &c.kvFilter,
		Default: "",
		Usage:   "Can only be used with -kvdetails. Limits KV key breakdown using this prefix filter.",
	})

	return set
}

func (c *OperatorRaftSnapshotInspectCommand) AutocompleteArgs() complete.Predictor {
	return complete.PredictAnything
}

func (c *OperatorRaftSnapshotInspectCommand) AutocompleteFlags() complete.Flags {
	return c.Flags().Completions()
}

func (c *OperatorRaftSnapshotInspectCommand) Run(args []string) int {
	// TODO: how to add other flags like kvdetails, kvfilter and kvdepth
	flags := c.Flags()

	if err := flags.Parse(args); err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	var file string
	args = c.flags.Args()

	switch len(args) {
	case 0:
		c.UI.Error("Missing FILE argument")
		return 1
	case 1:
		file = args[0]
	default:
		c.UI.Error(fmt.Sprintf("Too many arguments (expected 1, got %d)", len(args)))
		return 1
	}

	// TODO: skipping state.bin logic for now
	// Open the file.
	f, err := os.Open(file)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error opening snapshot file: %s", err))
		return 1
	}
	defer f.Close()

	var readFile *os.File
	var meta *raft.SnapshotMeta

	readFile, meta, err = Read(hclog.New(nil), f)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error reading snapshot: %s", err))
		return 1
	}
	defer func() {
		if err := readFile.Close(); err != nil {
			c.UI.Error(fmt.Sprintf("Failed to close temp snapshot: %v", err))
		}
		if err := os.Remove(readFile.Name()); err != nil {
			c.UI.Error(fmt.Sprintf("Failed to clean up temp snapshot: %v", err))
		}
	}()

	info, err := c.enhance(readFile)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error extracting snapshot data: %s", err))
		return 1
	}

	formatter, err := NewFormatter("pretty")
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error outputting enhanced snapshot data: %s", err))
		return 1
	}
	// Generate structs for the formatter with information we read in
	metaformat := &MetadataInfo{
		ID:      meta.ID,
		Size:    meta.Size,
		Index:   meta.Index,
		Term:    meta.Term,
		Version: meta.Version,
	}

	// Restructures stats given above to be human readable
	// Note: v1 does not calculate these stats
	// formattedStats := generateStats(info)
	formattedStatsKV := generateKVStats(info)

	in := &OutputFormat{
		Meta: metaformat,
		// Note: v1 does not calculate stats
		// Stats:       formattedStats,
		StatsKV:      formattedStatsKV,
		TotalSize:    info.TotalSize,
		TotalCountKV: info.TotalCountKV,
	}

	out, err := formatter.Format(in)
	if err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	c.UI.Output(out)
	return 0
}

func (c *OperatorRaftSnapshotInspectCommand) kvEnhance(val *pb.StorageEntry, info *SnapshotInfo) {
	if c.kvDetails {

		// TODO: add this as option
		// if c.kvDetails {
		if val.Key == "" {
			return
		}

		// check for whether a filter is specified. if it is, skip
		// any keys that don't match.
		fmt.Println("Key", val.Key)
		fmt.Println("Filter", c.kvFilter)
		if len(c.kvFilter) > 0 && !strings.HasPrefix(val.Key, c.kvFilter) {
			fmt.Println("filtering out")
			return
		}

		split := strings.Split(string(val.Key), "/")

		// handle the situation where the key is shorter than
		// the specified depth.
		actualDepth := c.kvDepth
		if c.kvDepth > len(split) {
			actualDepth = len(split)
		}

		prefix := strings.Join(split[0:actualDepth], "/")
		kvs := info.StatsKV[prefix]
		if kvs.Name == "" {
			kvs.Name = prefix
		}

		kvs.Count++
		info.TotalCountKV++
		info.StatsKV[prefix] = kvs
	}
}

func (c *OperatorRaftSnapshotInspectCommand) enhance(file io.Reader) (SnapshotInfo, error) {
	info := SnapshotInfo{
		// we are not calculating these stats in v1
		// Stats:       make(map[uint8]typeStats),
		StatsKV:      make(map[string]typeStats),
		TotalSize:    0,
		TotalCountKV: 0,
	}

	handler := func(s *pb.StorageEntry) error {
		// name := string(msg)
		// s := info.Stats[msg]
		// if s.Name == "" {
		// 	s.Name = name
		// }
		// size := cr.read - info.TotalSize
		// s.Sum += size
		// s.Count++
		// info.TotalSize = cr.read
		// info.Stats[msg] = s

		c.kvEnhance(s, &info)

		return nil
	}

	_, err := ReadSnapshot(file, handler)
	if err != nil {
		return info, err
	}

	return info, nil
}

// ReadSnapshot decodes each message type and utilizes the handler function to
// process each message type individually
func ReadSnapshot(r io.Reader, handler func(s *pb.StorageEntry) error) (*iradix.Tree, error) {
	protoReader := protoio.NewDelimitedReader(r, math.MaxInt32)

	errCh := make(chan error, 1)

	txn := iradix.New().Txn()

	go func() {
		for {
			s := new(pb.StorageEntry)

			err := protoReader.ReadMsg(s)

			// TODO: call handler here to calculate info stats
			handler(s)

			if err != nil {
				if err == io.EOF {

					errCh <- nil
					return
				}
				errCh <- err
				return
			}

			var value interface{} = struct{}{}

			// TODO: assuming we want to load values from right now
			// if loadValues {
			// 	value = s.Value
			// }
			value = s.Value

			txn.Insert([]byte(s.Key), value)
		}
	}()

	err := <-errCh
	if err != nil && err != io.EOF {
		return nil, err
	}

	data := txn.Commit()

	return data, nil
}

func NewFormatter(format string) (ConsulFormatter, error) {
	switch format {
	case PrettyFormat:
		return newPrettyFormatter(), nil
	case JSONFormat:
		return newJSONFormatter(), nil
	default:
		return nil, fmt.Errorf("Unknown format: %s", format)
	}
}

const (
	PrettyFormat string = "pretty"
	JSONFormat   string = "json"
)

type ConsulFormatter interface {
	Format(*OutputFormat) (string, error)
}

func newPrettyFormatter() ConsulFormatter {
	return &prettyFormatter{}
}

type prettyFormatter struct{}

type jsonFormatter struct{}

func newJSONFormatter() ConsulFormatter {
	return &jsonFormatter{}
}

func (_ *jsonFormatter) Format(info *OutputFormat) (string, error) {
	b, err := json.MarshalIndent(info, "", "   ")
	if err != nil {
		return "", fmt.Errorf("Failed to marshal original snapshot stats: %v", err)
	}
	return string(b), nil
}

func (_ *prettyFormatter) Format(info *OutputFormat) (string, error) {
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 8, 8, 6, ' ', 0)

	fmt.Fprintf(tw, " ID\t%s", info.Meta.ID)
	fmt.Fprintf(tw, "\n Size\t%d", info.Meta.Size)
	fmt.Fprintf(tw, "\n Index\t%d", info.Meta.Index)
	fmt.Fprintf(tw, "\n Term\t%d", info.Meta.Term)
	fmt.Fprintf(tw, "\n Version\t%d", info.Meta.Version)
	fmt.Fprintf(tw, "\n")
	// fmt.Fprintln(tw, "\n Type\tCount\tSize")
	// fmt.Fprintf(tw, " %s\t%s\t%s", "----", "----", "----")
	// // For each different type generate new output
	// for _, s := range info.Stats {
	// 	fmt.Fprintf(tw, "\n %s\t%d\t%s", s.Name, s.Count, ByteSize(uint64(s.Sum)))
	// }
	// fmt.Fprintf(tw, "\n %s\t%s\t%s", "----", "----", "----")
	// fmt.Fprintf(tw, "\n Total\t\t%s", ByteSize(uint64(info.TotalSize)))

	if info.StatsKV != nil {
		fmt.Fprintf(tw, "\n")
		fmt.Fprintln(tw, "\n Key Name\tCount")
		fmt.Fprintf(tw, " %s\t%s", "----", "----")
		// For each different type generate new output
		for _, s := range info.StatsKV {
			fmt.Fprintf(tw, "\n %s\t%d", s.Name, s.Count)
		}
		fmt.Fprintf(tw, "\n %s\t%s", "----", "----")
		fmt.Fprintf(tw, "\n Total\t%d", info.TotalCountKV)
	}

	if err := tw.Flush(); err != nil {
		return b.String(), err
	}

	return b.String(), nil
}

// OutputFormat is used for passing information
// through the formatter
type OutputFormat struct {
	Meta      *MetadataInfo
	Stats     []typeStats
	StatsKV   []typeStats
	TotalSize int
	// not supported in v1
	// TotalSizeKV  int
	TotalCountKV int
}

const (
	BYTE = 1 << (10 * iota)
	KILOBYTE
	MEGABYTE
	GIGABYTE
	TERABYTE
)

func ByteSize(bytes uint64) string {
	unit := ""
	value := float64(bytes)

	switch {
	case bytes >= TERABYTE:
		unit = "TB"
		value = value / TERABYTE
	case bytes >= GIGABYTE:
		unit = "GB"
		value = value / GIGABYTE
	case bytes >= MEGABYTE:
		unit = "MB"
		value = value / MEGABYTE
	case bytes >= KILOBYTE:
		unit = "KB"
		value = value / KILOBYTE
	case bytes >= BYTE:
		unit = "B"
	case bytes == 0:
		return "0"
	}

	result := strconv.FormatFloat(value, 'f', 1, 64)
	result = strings.TrimSuffix(result, ".0")
	return result + unit
}

// Note: V1 will not calculate these stats
// generateStats formats the stats for the output struct
// that's used to produce the printed output the user sees.
// func generateStats(info SnapshotInfo) []typeStats {
// 	ss := make([]typeStats, 0, len(info.Stats))

// 	for _, s := range info.Stats {
// 		ss = append(ss, s)
// 	}

// 	ss = sortTypeStats(ss)

// 	return ss
// }

// sortTypeStats sorts the stat slice by size and then
// alphabetically in the case the size is identical
func sortTypeStats(stats []typeStats) []typeStats {
	// sort alphabetically if size is equal
	sort.Slice(stats, func(i, j int) bool {
		// Sort alphabetically if count is equal
		if stats[i].Count == stats[j].Count {
			return stats[i].Name < stats[j].Name
		}
		return stats[i].Count > stats[j].Count
	})

	return stats
}

// generateKVStats reformats the KV stats to work with
// the output struct that's used to produce the printed
// output the user sees.
func generateKVStats(info SnapshotInfo) []typeStats {
	kvLen := len(info.StatsKV)
	if kvLen > 0 {
		ks := make([]typeStats, 0, kvLen)

		for _, s := range info.StatsKV {
			ks = append(ks, s)
		}

		ks = sortTypeStats(ks)

		return ks
	}

	return nil
}

// Read a snapshot into a temporary file. The caller is responsible for removing the file.
func Read(logger hclog.Logger, in io.Reader) (*os.File, *raft.SnapshotMeta, error) {
	// Wrap the reader in a gzip decompressor.
	decomp, err := gzip.NewReader(in)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decompress snapshot: %v", err)
	}
	defer func() {
		if err := decomp.Close(); err != nil {
			logger.Error("Failed to close snapshot decompressor", "error", err)
		}
	}()

	// Make a scratch file to receive the contents of the snapshot data so
	// we can avoid buffering in memory.
	snap, err := os.CreateTemp("", "snapshot")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create temp snapshot file: %v", err)
	}

	// Read the archive.
	var metadata raft.SnapshotMeta
	if err := read(decomp, &metadata, snap); err != nil {
		return nil, nil, fmt.Errorf("failed to read snapshot file: %v", err)
	}

	if err := concludeGzipRead(decomp); err != nil {
		return nil, nil, err
	}

	// Sync and rewind the file so it's ready to be read again.
	if err := snap.Sync(); err != nil {
		return nil, nil, fmt.Errorf("failed to sync temp snapshot: %v", err)
	}
	if _, err := snap.Seek(0, 0); err != nil {
		return nil, nil, fmt.Errorf("failed to rewind temp snapshot: %v", err)
	}
	return snap, &metadata, nil
}

// read takes a reader and extracts the snapshot metadata and the snapshot
// itself, and also checks the integrity of the data. You must arrange to call
// Close() on the returned object or else you will leak a temporary file.
func read(in io.Reader, metadata *raft.SnapshotMeta, snap io.Writer) error {
	// Start a new tar reader.
	archive := tar.NewReader(in)

	// Create a hash list that we will use to compare with the SHA256SUMS
	// file in the archive.
	hl := newHashList()

	// Populate the hashes for all the files we expect to see. The check at
	// the end will make sure these are all present in the SHA256SUMS file
	// and that the hashes match.
	// TODO: look into this verification process more carefully
	metaHash := hl.Add("meta.json")
	snapHash := hl.Add("state.bin")

	// Look through the archive for the pieces we care about.
	var shaBuffer bytes.Buffer
	var sealedSHABuffer bytes.Buffer
	for {
		hdr, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed reading snapshot: %v", err)
		}

		switch hdr.Name {
		case "meta.json":
			// Previously we used json.Decode to decode the archive stream. There are
			// edgecases in which it doesn't read all the bytes from the stream, even
			// though the json object is still being parsed properly. Since we
			// simultaneously feeded everything to metaHash, our hash ended up being
			// different than what we calculated when creating the snapshot. Which in
			// turn made the snapshot verification fail. By explicitly reading the
			// whole thing first we ensure that we calculate the correct hash
			// independent of how json.Decode works internally.
			buf, err := io.ReadAll(io.TeeReader(archive, metaHash))
			if err != nil {
				return fmt.Errorf("failed to read snapshot metadata: %v", err)
			}
			if err := json.Unmarshal(buf, &metadata); err != nil {
				return fmt.Errorf("failed to decode snapshot metadata: %v", err)
			}
		case "state.bin":
			if _, err := io.Copy(io.MultiWriter(snap, snapHash), archive); err != nil {
				return fmt.Errorf("failed to read or write snapshot data: %v", err)
			}
		case "SHA256SUMS":
			if _, err := io.Copy(&shaBuffer, archive); err != nil {
				return fmt.Errorf("failed to read snapshot hashes: %v", err)
			}

		case "SHA256SUMS.sealed":
			// TODO: do we need this special func or can we just copy
			if err := copyEOFOrN(&sealedSHABuffer, archive, 8192); err != nil {
				return fmt.Errorf("failed to read snapshot hashes: %v", err)
			}

		default:
			return fmt.Errorf("unexpected file %q in snapshot", hdr.Name)
		}
	}

	// Verify all the hashes.
	if err := hl.DecodeAndVerify(&shaBuffer); err != nil {
		return fmt.Errorf("failed checking integrity of snapshot: %v", err)
	}

	// TODO: also verify sha256sums.sealed
	// opened, err := sealer.Open(context.Background(), sealedSHABuffer.Bytes())
	// if err != nil {
	// 	return fmt.Errorf("failed to open the sealed hashes: %v", err)
	// }
	// // Verify all the hashes.
	// if err := hl.DecodeAndVerify(bytes.NewBuffer(opened)); err != nil {
	// 	return fmt.Errorf("failed checking integrity of snapshot: %v", err)
	// }

	return nil
}

// copyEOFOrN copies until either EOF or maxBytesToRead was hit, or an error
// occurs. If a non-EOF error occurs, return it
func copyEOFOrN(dst io.Writer, src io.Reader, maxBytesToCopy int64) error {
	copied, err := io.CopyN(dst, src, maxBytesToCopy)
	if err == io.EOF {
		return nil
	}
	if copied == maxBytesToCopy {
		return fmt.Errorf("read max specified bytes (%d) without EOF - possible truncation", copied)
	}

	return err
}

// newHashList returns a new hashList.
func newHashList() *hashList {
	return &hashList{
		hashes: make(map[string]hash.Hash),
	}
}

// hashList manages a list of filenames and their hashes.
type hashList struct {
	hashes map[string]hash.Hash
}

// concludeGzipRead should be invoked after you think you've consumed all of
// the data from the gzip stream. It will error if the stream was corrupt.
//
// The docs for gzip.Reader say: "Clients should treat data returned by Read as
// tentative until they receive the io.EOF marking the end of the data."
func concludeGzipRead(decomp *gzip.Reader) error {
	extra, err := io.ReadAll(decomp) // ReadAll consumes the EOF
	if err != nil {
		return err
	} else if len(extra) != 0 {
		return fmt.Errorf("%d unread uncompressed bytes remain", len(extra))
	}
	return nil
}

// DecodeAndVerify reads a SHA256SUMS-style text file and checks the results
// against the current sums for all the hashes.
func (hl *hashList) DecodeAndVerify(r io.Reader) error {
	// Read the file and make sure everything in there has a matching hash.
	seen := make(map[string]struct{})
	s := bufio.NewScanner(r)
	for s.Scan() {
		sha := make([]byte, sha256.Size)
		var file string
		if _, err := fmt.Sscanf(s.Text(), "%x  %s", &sha, &file); err != nil {
			return err
		}

		h, ok := hl.hashes[file]
		if !ok {
			return fmt.Errorf("list missing hash for %q", file)
		}
		if !bytes.Equal(sha, h.Sum([]byte{})) {
			return fmt.Errorf("hash check failed for %q", file)
		}
		seen[file] = struct{}{}
	}
	if err := s.Err(); err != nil {
		return err
	}

	// Make sure everything we had a hash for was seen.
	for file := range hl.hashes {
		if _, ok := seen[file]; !ok {
			return fmt.Errorf("file missing for %q", file)
		}
	}

	return nil
}

// Add creates a new hash for the given file.
func (hl *hashList) Add(file string) hash.Hash {
	if existing, ok := hl.hashes[file]; ok {
		return existing
	}

	h := sha256.New()
	hl.hashes[file] = h
	return h
}

// Encode takes the current sum of all the hashes and saves the hash list as a
// SHA256SUMS-style text file.
func (hl *hashList) Encode(w io.Writer) error {
	for file, h := range hl.hashes {
		if _, err := fmt.Fprintf(w, "%x  %s\n", h.Sum([]byte{}), file); err != nil {
			return err
		}
	}
	return nil
}

type MessageType uint8

const (
	RegisterRequestType             MessageType = 0
	DeregisterRequestType                       = 1
	KVSRequestType                              = 2
	SessionRequestType                          = 3
	DeprecatedACLRequestType                    = 4 // Removed with the legacy ACL system
	TombstoneRequestType                        = 5
	CoordinateBatchUpdateType                   = 6
	PreparedQueryRequestType                    = 7
	TxnRequestType                              = 8
	AutopilotRequestType                        = 9
	AreaRequestType                             = 10
	ACLBootstrapRequestType                     = 11
	IntentionRequestType                        = 12
	ConnectCARequestType                        = 13
	ConnectCAProviderStateType                  = 14
	ConnectCAConfigType                         = 15 // FSM snapshots only.
	IndexRequestType                            = 16 // FSM snapshots only.
	ACLTokenSetRequestType                      = 17
	ACLTokenDeleteRequestType                   = 18
	ACLPolicySetRequestType                     = 19
	ACLPolicyDeleteRequestType                  = 20
	ConnectCALeafRequestType                    = 21
	ConfigEntryRequestType                      = 22
	ACLRoleSetRequestType                       = 23
	ACLRoleDeleteRequestType                    = 24
	ACLBindingRuleSetRequestType                = 25
	ACLBindingRuleDeleteRequestType             = 26
	ACLAuthMethodSetRequestType                 = 27
	ACLAuthMethodDeleteRequestType              = 28
	ChunkingStateType                           = 29
	FederationStateRequestType                  = 30
	SystemMetadataRequestType                   = 31
	ServiceVirtualIPRequestType                 = 32
	FreeVirtualIPRequestType                    = 33
	KindServiceNamesType                        = 34
	PeeringWriteType                            = 35
	PeeringDeleteType                           = 36
	PeeringTerminateByIDType                    = 37
	PeeringTrustBundleWriteType                 = 38
	PeeringTrustBundleDeleteType                = 39
	PeeringSecretsWriteType                     = 40
	RaftLogVerifierCheckpoint                   = 41 // Only used for log verifier, no-op on FSM.
	ResourceOperationType                       = 42
	UpdateVirtualIPRequestType                  = 43
)
