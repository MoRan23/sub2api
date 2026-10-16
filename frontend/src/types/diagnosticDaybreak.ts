export interface DiagnosticDaybreakPreferences {
  blue: boolean
  red: boolean
}

// Evidence from the successful outbound probe, never inferred from current settings.
export interface DiagnosticDaybreakEvidence {
  cyber: string
  reason: string
}
