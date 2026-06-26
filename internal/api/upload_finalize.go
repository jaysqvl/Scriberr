package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"scriberr/internal/audio"
	"scriberr/internal/models"
	"scriberr/internal/transcription"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type assembledUploadFile struct {
	ID           string
	Role         models.UploadFileRole
	OriginalName string
	ContentType  string
	Path         string
	Size         int64
}

func (h *Handler) finalizeAssembledUpload(c *gin.Context, session *models.UploadSession, files []assembledUploadFile) (string, string, interface{}, error) {
	title := ""
	if session.Title != nil {
		title = *session.Title
	}

	switch session.Kind {
	case models.UploadKindAudio:
		file, err := singleAssembledFile(files, models.UploadFileRoleAudio)
		if err != nil {
			return "", "", nil, err
		}
		path, err := h.moveAssembledToUpload(file)
		if err != nil {
			return "", "", nil, err
		}
		job, err := h.createUploadedAudioJob(c, path, title)
		if err != nil {
			return "", "", nil, err
		}
		return job.ID, "transcription", job, nil
	case models.UploadKindVideo:
		file, err := singleAssembledFile(files, models.UploadFileRoleVideo)
		if err != nil {
			return "", "", nil, err
		}
		path, err := h.moveAssembledToUpload(file)
		if err != nil {
			return "", "", nil, err
		}
		job, err := h.createUploadedVideoJob(c, path, title)
		if err != nil {
			return "", "", nil, err
		}
		return job.ID, "transcription", job, nil
	case models.UploadKindQuick:
		file, err := singleAssembledFile(files, models.UploadFileRoleAudio)
		if err != nil {
			return "", "", nil, err
		}
		params, err := h.quickParamsForUploadSession(c.Request.Context(), session)
		if err != nil {
			return "", "", nil, err
		}
		quickJob, err := h.createQuickTranscriptionFromPath(file.Path, file.OriginalName, params)
		if err != nil {
			return "", "", nil, err
		}
		return quickJob.ID, "quick", quickJob, nil
	case models.UploadKindSubmit:
		file, err := singleAssembledFile(files, models.UploadFileRoleAudio)
		if err != nil {
			return "", "", nil, err
		}
		path, err := h.moveAssembledToUpload(file)
		if err != nil {
			return "", "", nil, err
		}
		job, err := h.createSubmittedJobFromPath(c, path, title, session.ParametersJSON)
		if err != nil {
			return "", "", nil, err
		}
		return job.ID, "transcription", job, nil
	case models.UploadKindMultiTrack:
		aup, err := singleAssembledFile(files, models.UploadFileRoleAup)
		if err != nil {
			return "", "", nil, err
		}
		tracks := filesByRole(files, models.UploadFileRoleTrack)
		if len(tracks) == 0 {
			return "", "", nil, fmt.Errorf("Multi-track upload has no tracks")
		}
		job, err := h.createMultiTrackJobFromPaths(c, title, aup, tracks)
		if err != nil {
			return "", "", nil, err
		}
		return job.ID, "transcription", job, nil
	default:
		return "", "", nil, fmt.Errorf("Unsupported upload kind")
	}
}

func (h *Handler) respondWithCompletedUpload(c *gin.Context, session *models.UploadSession) {
	switch {
	case session.ResultType != nil && *session.ResultType == "quick" && session.ResultID != nil:
		job, err := h.quickTranscription.GetQuickJob(*session.ResultID)
		if err == nil {
			c.JSON(http.StatusOK, job)
			return
		}
	case session.ResultID != nil:
		job, err := h.jobRepo.FindWithAssociations(c.Request.Context(), *session.ResultID)
		if err == nil {
			c.JSON(http.StatusOK, job)
			return
		}
	}
	c.JSON(http.StatusOK, buildUploadSessionResponse(*session, ""))
}

func (h *Handler) createUploadedAudioJob(c *gin.Context, filePath, title string) (*models.TranscriptionJob, error) {
	finalPath, err := h.convertWebMToMP3IfNeeded(filePath)
	if err != nil {
		_ = h.fileService.RemoveFile(filePath)
		return nil, err
	}

	jobID := filenameWithoutExt(finalPath)
	job := models.TranscriptionJob{
		ID:        jobID,
		AudioPath: finalPath,
		Status:    models.StatusUploaded,
	}
	if strings.TrimSpace(title) != "" {
		job.Title = stringPtr(strings.TrimSpace(title))
	}

	if err := h.jobRepo.Create(c.Request.Context(), &job); err != nil {
		_ = h.fileService.RemoveFile(finalPath)
		return nil, fmt.Errorf("Failed to create job")
	}

	h.applyAutoTranscription(c, &job)
	return &job, nil
}

func (h *Handler) createUploadedVideoJob(c *gin.Context, videoPath, title string) (*models.TranscriptionJob, error) {
	jobID := filenameWithoutExt(videoPath)
	audioPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".mp3"

	cmd := exec.Command("ffmpeg", "-i", videoPath, "-vn", "-acodec", "libmp3lame", "-q:a", "2", audioPath)
	if err := cmd.Run(); err != nil {
		_ = h.fileService.RemoveFile(videoPath)
		return nil, fmt.Errorf("Failed to extract audio from video")
	}

	job := models.TranscriptionJob{
		ID:        jobID,
		AudioPath: audioPath,
		Status:    models.StatusUploaded,
	}
	if strings.TrimSpace(title) != "" {
		job.Title = stringPtr(strings.TrimSpace(title))
	}

	if err := h.jobRepo.Create(c.Request.Context(), &job); err != nil {
		_ = h.fileService.RemoveFile(videoPath)
		_ = h.fileService.RemoveFile(audioPath)
		return nil, fmt.Errorf("Failed to create job")
	}

	_ = h.fileService.RemoveFile(videoPath)
	h.applyAutoTranscription(c, &job)
	return &job, nil
}

func (h *Handler) createSubmittedJobFromPath(c *gin.Context, filePath, title string, parametersJSON *string) (*models.TranscriptionJob, error) {
	params := defaultSubmitParams()
	if parametersJSON != nil && strings.TrimSpace(*parametersJSON) != "" {
		if err := json.Unmarshal([]byte(*parametersJSON), &params); err != nil {
			return nil, fmt.Errorf("Invalid parameters JSON")
		}
	}
	return h.createSubmittedJobWithParams(c, filePath, title, params)
}

func (h *Handler) createSubmittedJobWithParams(c *gin.Context, filePath, title string, params models.WhisperXParams) (*models.TranscriptionJob, error) {
	jobID := filenameWithoutExt(filePath)
	job := models.TranscriptionJob{
		ID:          jobID,
		AudioPath:   filePath,
		Status:      models.StatusPending,
		Diarization: params.Diarize,
		Parameters:  params,
	}
	if strings.TrimSpace(title) != "" {
		job.Title = stringPtr(strings.TrimSpace(title))
	}

	if err := h.jobRepo.Create(c.Request.Context(), &job); err != nil {
		_ = h.fileService.RemoveFile(filePath)
		return nil, fmt.Errorf("Failed to create job")
	}
	if err := h.taskQueue.EnqueueJob(jobID); err != nil {
		return nil, fmt.Errorf("Failed to enqueue job")
	}
	return &job, nil
}

func submitParamsFromForm(c *gin.Context) (models.WhisperXParams, error) {
	diarize := false
	if v := c.PostForm("diarization"); v != "" {
		diarize = strings.EqualFold(v, "true") || v == "1"
	} else {
		diarize = getFormBoolWithDefault(c, "diarize", false)
	}

	params := models.WhisperXParams{
		Model:       getFormValueWithDefault(c, "model", "base"),
		BatchSize:   getFormIntWithDefault(c, "batch_size", 16),
		ComputeType: getFormValueWithDefault(c, "compute_type", "int8"),
		Device:      getFormValueWithDefault(c, "device", "cpu"),
		VadOnset:    getFormFloatWithDefault(c, "vad_onset", 0.500),
		VadOffset:   getFormFloatWithDefault(c, "vad_offset", 0.363),
		Diarize:     diarize,
	}

	if lang := c.PostForm("language"); lang != "" {
		params.Language = &lang
	}

	if minSpeakers := c.PostForm("min_speakers"); minSpeakers != "" {
		if min, err := strconv.Atoi(minSpeakers); err == nil {
			params.MinSpeakers = &min
		}
	}

	if maxSpeakers := c.PostForm("max_speakers"); maxSpeakers != "" {
		if max, err := strconv.Atoi(maxSpeakers); err == nil {
			params.MaxSpeakers = &max
		}
	}

	if hfToken := c.PostForm("hf_token"); hfToken != "" {
		params.HfToken = &hfToken
	}

	diarizeModel := getFormValueWithDefault(c, "diarize_model", "pyannote")
	if diarizeModel != "pyannote" && diarizeModel != "nvidia_sortformer" {
		return models.WhisperXParams{}, fmt.Errorf("Invalid diarize_model. Must be 'pyannote' or 'nvidia_sortformer'")
	}
	params.DiarizeModel = diarizeModel

	return params, nil
}

func (h *Handler) createMultiTrackJobFromPaths(c *gin.Context, title string, aup assembledUploadFile, tracks []assembledUploadFile) (*models.TranscriptionJob, error) {
	jobID := uuidString()
	jobDir := filepath.Join(h.config.UploadDir, jobID)
	if err := h.fileService.CreateDirectory(jobDir); err != nil {
		return nil, fmt.Errorf("Failed to create job directory")
	}

	aupPath := filepath.Join(jobDir, "project-"+safeFilename(aup.OriginalName))
	if err := moveOrCopyFile(aup.Path, aupPath); err != nil {
		_ = h.fileService.RemoveDirectory(jobDir)
		return nil, fmt.Errorf("Failed to save AUP file")
	}

	trackInfoByName := map[string]audio.AupTrack{}
	if parsedTracks, err := audio.NewAupParser().ParseAupFile(aupPath); err == nil {
		for _, parsedTrack := range parsedTracks {
			trackInfoByName[filepath.Base(parsedTrack.Filename)] = parsedTrack
		}
	}

	trackFiles := make([]models.MultiTrackFile, 0, len(tracks))
	usedNames := map[string]int{}
	for i, track := range tracks {
		originalBase := filepath.Base(track.OriginalName)
		safeBase := uniqueSafeFilename(originalBase, usedNames)
		trackPath := filepath.Join(jobDir, safeBase)
		if err := moveOrCopyFile(track.Path, trackPath); err != nil {
			_ = h.fileService.RemoveDirectory(jobDir)
			return nil, fmt.Errorf("Failed to save track %s", originalBase)
		}

		trackFile := models.MultiTrackFile{
			TranscriptionJobID: jobID,
			FilePath:           trackPath,
			FileName:           originalBase,
			TrackIndex:         i,
			Offset:             0,
			Gain:               1,
			Pan:                0,
			Mute:               false,
		}
		if parsedTrack, ok := trackInfoByName[originalBase]; ok {
			trackFile.Offset = parsedTrack.Offset
			trackFile.Gain = parsedTrack.Gain
			trackFile.Pan = parsedTrack.Pan
			trackFile.Mute = parsedTrack.Mute == 1
		}
		trackFiles = append(trackFiles, trackFile)
	}

	jobTitle := strings.TrimSpace(title)
	if jobTitle == "" {
		jobTitle = fmt.Sprintf("Multi-track Job %s", jobID)
	}
	mergeStatus := "pending"
	job := models.TranscriptionJob{
		ID:               jobID,
		Title:            &jobTitle,
		AudioPath:        filepath.Join(jobDir, "merged.mp3"),
		Status:           models.StatusUploaded,
		IsMultiTrack:     true,
		AupFilePath:      &aupPath,
		MultiTrackFolder: &jobDir,
		MergeStatus:      mergeStatus,
		MultiTrackFiles:  trackFiles,
	}

	if err := h.jobRepo.Create(c.Request.Context(), &job); err != nil {
		_ = h.fileService.RemoveDirectory(jobDir)
		return nil, fmt.Errorf("Failed to create job")
	}
	return &job, nil
}

func (h *Handler) applyAutoTranscription(c *gin.Context, job *models.TranscriptionJob) {
	userID, exists := c.Get("user_id")
	if !exists {
		return
	}
	user, err := h.userService.GetUser(c.Request.Context(), userID.(uint))
	if err != nil || !user.AutoTranscriptionEnabled {
		return
	}

	var profile *models.TranscriptionProfile
	if user.DefaultProfileID != nil {
		profile, _ = h.profileRepo.FindByID(c.Request.Context(), *user.DefaultProfileID)
	}
	if profile == nil {
		profile, _ = h.profileRepo.FindDefault(c.Request.Context())
	}
	if profile == nil {
		profiles, _, _ := h.profileRepo.List(c.Request.Context(), 0, 1)
		if len(profiles) > 0 {
			profile = &profiles[0]
		}
	}
	if profile == nil {
		return
	}

	job.Parameters = profile.Parameters
	job.Diarization = profile.Parameters.Diarize
	job.Status = models.StatusPending
	if err := h.jobRepo.Update(c.Request.Context(), job); err == nil {
		if err := h.taskQueue.EnqueueJob(job.ID); err != nil {
			job.Status = models.StatusUploaded
			_ = h.jobRepo.Update(c.Request.Context(), job)
		}
	}
}

func (h *Handler) convertWebMToMP3IfNeeded(filePath string) (string, error) {
	if strings.ToLower(filepath.Ext(filePath)) != ".webm" {
		return filePath, nil
	}

	mp3Path := strings.TrimSuffix(filePath, filepath.Ext(filePath)) + ".mp3"
	cmd := exec.Command("ffmpeg", "-i", filePath, "-vn", "-af", "loudnorm", "-acodec", "libmp3lame", "-b:a", "320k", mp3Path)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Failed to convert WebM audio to MP3")
	}
	_ = h.fileService.RemoveFile(filePath)
	return mp3Path, nil
}

func (h *Handler) quickParamsForUploadSession(ctx context.Context, session *models.UploadSession) (models.WhisperXParams, error) {
	if session.ProfileName != nil && strings.TrimSpace(*session.ProfileName) != "" {
		profile, err := h.profileRepo.FindByName(ctx, strings.TrimSpace(*session.ProfileName))
		if err != nil {
			return models.WhisperXParams{}, fmt.Errorf("Profile %q not found", *session.ProfileName)
		}
		return profile.Parameters, nil
	}
	params := defaultQuickTranscriptionParams()
	if session.ParametersJSON != nil && strings.TrimSpace(*session.ParametersJSON) != "" {
		if err := json.Unmarshal([]byte(*session.ParametersJSON), &params); err != nil {
			return models.WhisperXParams{}, fmt.Errorf("Invalid parameters JSON")
		}
	}
	return params, nil
}

func (h *Handler) createQuickTranscriptionFromPath(path, filename string, params models.WhisperXParams) (*transcription.QuickTranscriptionJob, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Failed to open uploaded audio")
	}
	defer file.Close()

	job, err := h.quickTranscription.SubmitQuickJob(file, filename, params)
	if err != nil {
		return nil, fmt.Errorf("Failed to submit quick transcription: %v", err)
	}
	return job, nil
}

func (h *Handler) moveAssembledToUpload(file assembledUploadFile) (string, error) {
	if err := h.fileService.CreateDirectory(h.config.UploadDir); err != nil {
		return "", fmt.Errorf("Failed to create upload directory")
	}
	ext := filepath.Ext(file.OriginalName)
	if ext == "" {
		ext = filepath.Ext(file.Path)
	}
	dest := filepath.Join(h.config.UploadDir, uuidString()+ext)
	if err := moveOrCopyFile(file.Path, dest); err != nil {
		return "", fmt.Errorf("Failed to save uploaded file")
	}
	return dest, nil
}

func saveMultipartFileToPath(fileHeader *multipart.FileHeader, destPath string) error {
	src, err := fileHeader.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}
	dst, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func moveOrCopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

func singleAssembledFile(files []assembledUploadFile, role models.UploadFileRole) (assembledUploadFile, error) {
	matches := filesByRole(files, role)
	if len(matches) != 1 {
		return assembledUploadFile{}, fmt.Errorf("Expected exactly one %s file", role)
	}
	return matches[0], nil
}

func filesByRole(files []assembledUploadFile, role models.UploadFileRole) []assembledUploadFile {
	var matches []assembledUploadFile
	for _, file := range files {
		if file.Role == role {
			matches = append(matches, file)
		}
	}
	return matches
}

func defaultSubmitParams() models.WhisperXParams {
	return models.WhisperXParams{
		Model:        "base",
		BatchSize:    16,
		ComputeType:  "int8",
		Device:       "cpu",
		VadOnset:     0.500,
		VadOffset:    0.363,
		Diarize:      false,
		DiarizeModel: "pyannote",
	}
}

func defaultQuickTranscriptionParams() models.WhisperXParams {
	return models.WhisperXParams{
		Model:             "small",
		Device:            "cpu",
		DeviceIndex:       0,
		BatchSize:         8,
		ComputeType:       "float32",
		OutputFormat:      "all",
		Verbose:           true,
		Task:              "transcribe",
		InterpolateMethod: "nearest",
		VadMethod:         "pyannote",
		VadOnset:          0.5,
		VadOffset:         0.363,
		ChunkSize:         30,
		Diarize:           false,
		DiarizeModel:      "pyannote/speaker-diarization-3.1",
		Temperature:       0,
		BestOf:            5,
		BeamSize:          5,
		Patience:          1.0,
		LengthPenalty:     1.0,
		Fp16:              true,
		SegmentResolution: "sentence",
	}
}

func filenameWithoutExt(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func stringPtr(value string) *string {
	return &value
}

func safeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "" {
		return "file"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	clean := strings.TrimSpace(b.String())
	if clean == "" {
		return "file"
	}
	return clean
}

func uniqueSafeFilename(name string, used map[string]int) string {
	safe := safeFilename(name)
	count := used[safe]
	used[safe] = count + 1
	if count == 0 {
		return safe
	}
	ext := filepath.Ext(safe)
	stem := strings.TrimSuffix(safe, ext)
	return stem + "-" + strconv.Itoa(count+1) + ext
}

func uuidString() string {
	return uuid.New().String()
}
