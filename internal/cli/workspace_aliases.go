// Code moved to internal/workspace; these aliases keep the CLI reading as before.

package cli

import "github.com/creght-dev/creght-cli/internal/workspace"

type pullOutcome = workspace.PullOutcome
type workspaceState = workspace.WorkspaceState
type stateEntry = workspace.StateEntry
type snapshotEntry = workspace.SnapshotEntry
type Syncer = workspace.Syncer

const creghtIgnoreFileName = workspace.CreghtIgnoreFileName
const stateDirName = workspace.StateDirName
const baseDirName = workspace.BaseDirName
const conflictMarkerLocal = workspace.ConflictMarkerLocal
const conflictMarkerSep = workspace.ConflictMarkerSep
const conflictMarkerRemote = workspace.ConflictMarkerRemote

var (
	unifiedLineDiff                = workspace.UnifiedLineDiff
	canonicalAPIHost               = workspace.CanonicalAPIHost
	safePullWorkspace              = workspace.SafePullWorkspace
	writePulledFile                = workspace.WritePulledFile
	qetagHash                      = workspace.QetagHash
	normalizeSitePath              = workspace.NormalizeSitePath
	loadCreghtIgnore               = workspace.LoadCreghtIgnore
	ignoredRemotePaths             = workspace.IgnoredRemotePaths
	filterIgnoredSnapshot          = workspace.FilterIgnoredSnapshot
	filterIgnoredState             = workspace.FilterIgnoredState
	remoteFileSnapshotForWorkspace = workspace.RemoteFileSnapshotForWorkspace
	refuseSnapshotWorkspace        = workspace.RefuseSnapshotWorkspace
	parseSnapshotVersionNo         = workspace.ParseSnapshotVersionNo
	pullVersionSnapshot            = workspace.PullVersionSnapshot
	statePath                      = workspace.StatePath
	readBaseObject                 = workspace.ReadBaseObject
	loadWorkspaceState             = workspace.LoadWorkspaceState
	resolveSiteWorkspace           = workspace.ResolveSiteWorkspace
	findWorkspaceState             = workspace.FindWorkspaceState
	saveWorkspaceState             = workspace.SaveWorkspaceState
	dropStateFileEntry             = workspace.DropStateFileEntry
	putStateFileEntry              = workspace.PutStateFileEntry
	remoteFileSnapshot             = workspace.RemoteFileSnapshot
	localFileSnapshot              = workspace.LocalFileSnapshot
	buildSyncPlan                  = workspace.BuildSyncPlan
	buildPullEntryPlan             = workspace.BuildPullEntryPlan
	createFileAction               = workspace.CreateFileAction
	updateFileAction               = workspace.UpdateFileAction
	mergeStateSnapshot             = workspace.MergeStateSnapshot
	NewSyncer                      = workspace.NewSyncer
	newClientID                    = workspace.NewClientID
	printSyncPlan                  = workspace.PrintSyncPlan
	siteActionLabel                = workspace.SiteActionLabel
	deleteFileAction               = workspace.DeleteFileAction
	conflictJSONDetails            = workspace.ConflictJSONDetails
	remotePathToLocal              = workspace.RemotePathToLocal
	localPathToRemote              = workspace.LocalPathToRemote
	isPathInside                   = workspace.IsPathInside
	walkWorkspaceFiles             = workspace.WalkWorkspaceFiles
	writeRemoteFilesToWorkspace    = workspace.WriteRemoteFilesToWorkspace
	ensurePulledAgentsFile         = workspace.EnsurePulledAgentsFile
	writeBackupFiles               = workspace.WriteBackupFiles
	backupOverwrittenLocalFiles    = workspace.BackupOverwrittenLocalFiles
	shouldSkipLocalPath            = workspace.ShouldSkipLocalPath
	isUTF8FileBody                 = workspace.IsUTF8FileBody
	merge3                         = workspace.Merge3
	hasConflictMarkers             = workspace.HasConflictMarkers
	resolveConflictBody            = workspace.ResolveConflictBody
)
