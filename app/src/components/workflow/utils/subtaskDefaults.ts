import type { SchemaProperty } from './fieldTypeUtils';

/**
 * Initial params for a container sub-task whose action was just picked. Mirrors
 * the action sidebar's auto-fill: schema defaults are committed so they reach
 * the saved workflow, single-option required choices are picked, and
 * account_id is prefilled from the workflow's account.
 */
export function buildInitialSubTaskParams(taskDefinition: { input_schema?: Record<string, SchemaProperty> } | null | undefined, accountId?: string) {
  const params: Record<string, any> = {};
  const inputSchema = taskDefinition?.input_schema ?? {};
  for (const [fieldName, fieldSchema] of Object.entries(inputSchema)) {
    const defaultValue = fieldSchema?.default;
    if (defaultValue !== undefined && defaultValue !== null && defaultValue !== '') {
      params[fieldName] = defaultValue;
      continue;
    }
    // A required choice with a single option has only one valid answer
    const choices = fieldSchema?.options ?? fieldSchema?.enum;
    if (fieldSchema?.required && Array.isArray(choices) && choices.length === 1) {
      // An array field renders as a multi-select, which only reads list values
      params[fieldName] = fieldSchema.type === 'array' ? [choices[0]] : choices[0];
    }
  }
  if (accountId && inputSchema.account_id && !params.account_id) {
    params.account_id = accountId;
  }
  return params;
}
