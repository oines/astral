// Kept with the in-memory SessionState, so an evicted session also loses its
// reading position. Never persist message content or positions across logins.
export interface TranscriptPosition {
  following: boolean
  top: number
  anchor?: { key: string; node?: string; offset: number }
}
