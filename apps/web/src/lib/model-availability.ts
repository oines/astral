const reasons = new Set(['provider_missing', 'provider_disabled', 'model_disabled', 'model_missing', 'invalid_configuration', 'token_limits_unknown'])

export function modelUnavailableLabel(reason: string | undefined, t: (key: string) => string) {
  return reasons.has(reason || '') ? t(`modelUnavailable_${reason}`) : reason || t('unavailable')
}
