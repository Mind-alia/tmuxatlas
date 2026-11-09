// agy shows its /voice dictation below the rule that closes its prompt:
//   ● Recording 00:03  enter or f5 to insert · esc to discard
//     Finishing up... enter or f5 to insert what you have · esc to discard
// A failed dictation prints "No speech detected" in the conversation and leaves
// the prompt empty, so the prompt itself tells whether a transcript arrived.
const PROMPT_RULE = /─{10,}/
const RECORDING = /\bRecording \d{1,2}:\d{2}\b/
const PROCESSING = /\bFinishing up(\.\.\.|…)/

export type AgyVoiceStatus = 'recording' | 'processing' | 'idle'

function ruleIndexes(rows: string[]): number[] {
  return rows.flatMap((row, index) => (PROMPT_RULE.test(row) ? [index] : []))
}

export function readAgyVoiceStatus(rows: string[]): AgyVoiceStatus {
  const rules = ruleIndexes(rows)
  if (rules.length === 0) return 'idle'
  const footer = rows.slice(rules[rules.length - 1] + 1).join('\n')
  if (RECORDING.test(footer)) return 'recording'
  return PROCESSING.test(footer) ? 'processing' : 'idle'
}

/** Whether agy's prompt, between its two last rules, holds any text. */
export function agyPromptHasText(rows: string[]): boolean {
  const rules = ruleIndexes(rows)
  if (rules.length < 2) return false
  return rows
    .slice(rules[rules.length - 2] + 1, rules[rules.length - 1])
    .some(row => row.replace(/^\s*>/, '').trim() !== '')
}
