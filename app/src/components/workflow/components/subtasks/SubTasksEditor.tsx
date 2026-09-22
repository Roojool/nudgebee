import React, { useMemo, useRef, useState } from 'react';
import { Autocomplete, Box, IconButton, InputAdornment, TextField, Typography } from '@mui/material';
import { Label } from '@ui/Label';
import { Add, DragIndicator } from '@mui/icons-material';
import { DeleteIconRed } from '@assets';
import { Button } from '@ui/Button';
import { Modal } from '@ui/Modal';
import Accordion from '@ui/Accordion';
import ReorderableList from '@shared/ReorderableList';
import SafeIcon from '@shared/icons/SafeIcon';
import { ds } from 'src/utils/colors';
import type { TemplateSuggestion } from '../TemplateTextField';
import { StableTextField } from '../StableFormFields';
import { createTaskLabel, generateNodeCategories } from '../../constants/nodeCategories';
import { getTaskIcon, PROVIDER_COLOR_LOGOS } from '../../nodes/ActionNode';
import { validateTaskId } from '../../utils/taskUtils';
import { renderCategoryOrTaskIcon } from '../../utils/taskIcon';
import { buildInitialSubTaskParams } from '../../utils/subtaskDefaults';
import type { PreviousTask } from '../../utils/templateUtils';
import SubTaskParamForm from './SubTaskParamForm';

// Sub-task shape persisted in params.tasks. Extra keys (if / depends_on /
// timeout — YAML-authored) are preserved verbatim via the index signature.
export interface SubTask {
  id?: string;
  type?: string;
  params?: Record<string, any>;
  [key: string]: any;
}

// Backwards-compatible alias for the original foreach naming.
export type ForeachSubTask = SubTask;

// Action-picker option flattened from generateNodeCategories — same label,
// description and icon ("logo") the node palette shows for each task.
interface ActionOption {
  name: string;
  label: string;
  description?: string;
  icon: any;
  category: string;
  categoryIcon?: any;
  categoryColor?: string;
}

interface SubTasksEditorProps {
  value: SubTask[];
  onChange: (tasks: SubTask[]) => void;
  // Full validation error map; sub-task keys are prefixed `tasks[i].<field>`
  errors: Record<string, string>;
  taskDefinitions: any[];
  viewOnlyMode?: boolean;
  previousTasks: PreviousTask[];
  workflowInputs: Array<{ id: string; type: string; description?: string }>;
  workflowConfigs: Array<{ key: string; value: string; type: string }>;
  // Task types the accordion cannot host one level deep (containers, etc.)
  blockedTypes: Set<string>;
  // Container-scoped template suggestions (e.g. LoopItem for foreach); the
  // editor forwards them to each sub-task's param form. Defaults to none.
  extraSuggestions?: TemplateSuggestion[];
  // Distinguishes data-testids / uid namespace between containers ('foreach' | 'group')
  testIdPrefix: string;
  // Workflow account, prefilled into a newly picked action's account_id field
  accountId?: string;
  // Container-specific copy for the header caption and empty state
  copy: { helperText: React.ReactNode; emptyStateText: string };
}

// Same purple icon badge the canvas action nodes render (BaseNode's icon
// container + ActionNode's white-filter treatment), scaled to accordion size.
const SubTaskIconBadge: React.FC<{ taskType?: string; label: string }> = ({ taskType, label }) => {
  const icon = getTaskIcon(taskType ?? '');
  const isEmoji = typeof icon === 'string' && !icon.includes('/') && !icon.includes('.');
  const shouldKeepColors = PROVIDER_COLOR_LOGOS.includes(icon);
  return (
    <span
      style={{
        height: 24,
        width: 24,
        borderRadius: 'var(--ds-radius-lg)',
        backgroundColor: 'var(--ds-purple-500)',
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
      }}
    >
      {isEmoji ? (
        <span style={{ fontSize: 'var(--ds-text-small)' }}>{icon}</span>
      ) : (
        <SafeIcon
          src={icon}
          alt={label}
          width={14}
          height={14}
          style={{
            filter: shouldKeepColors ? 'none' : 'brightness(0) saturate(100%) invert(1)', // Force icon white
            objectFit: 'contain',
          }}
        />
      )}
    </span>
  );
};

/**
 * Accordion-based editor for a container node's `tasks` param (core.foreach /
 * core.group). Each sub-task is a reorderable row that expands to a name field,
 * an action (type) picker and a schema-driven parameter form. Container-specific
 * behaviour (blocked types, template suggestions, copy) is passed in via props.
 */
const SubTasksEditor: React.FC<SubTasksEditorProps> = ({
  value,
  onChange,
  errors,
  taskDefinitions,
  viewOnlyMode = false,
  previousTasks,
  workflowInputs,
  workflowConfigs,
  blockedTypes,
  extraSuggestions = [],
  testIdPrefix,
  copy,
  accountId,
}) => {
  // Stable per-row uids so React keys (and expansion state) survive
  // reorders — index keys would break TemplateTextField focus. Keyed by
  // sub-task object identity (WeakMap) rather than index so externally
  // replaced arrays (undo/redo) can't misattach state to the wrong row;
  // updateSubTask/handleAdd carry the uid across object copies.
  const uidCounter = useRef(0);
  const uidMap = useRef(new WeakMap<object, string>());
  const uids = useMemo(
    () =>
      value.map((sub, index) => {
        if (!sub || typeof sub !== 'object') return `${testIdPrefix}-sub-invalid-${index}`;
        let uid = uidMap.current.get(sub);
        if (!uid) {
          uid = `${testIdPrefix}-sub-${uidCounter.current++}`;
          uidMap.current.set(sub, uid);
        }
        return uid;
      }),
    [value, testIdPrefix]
  );

  const [expandedIds, setExpandedIds] = useState<string[]>([]);
  const [pendingDelete, setPendingDelete] = useState<number | null>(null);
  const [pendingTypeChange, setPendingTypeChange] = useState<{ index: number; newType: string } | null>(null);

  // Same labels/icons/descriptions as the node palette (generateNodeCategories
  // drives both), minus triggers, blocked containers and deprecated tasks.
  const actionOptions = useMemo<ActionOption[]>(() => {
    const categories = generateNodeCategories(taskDefinitions as any[]);
    const options: ActionOption[] = [];
    for (const [key, category] of Object.entries<any>(categories)) {
      if (key === 'triggers') continue;
      for (const [taskName, sub] of Object.entries<any>(category.subcategories ?? {})) {
        if (blockedTypes.has(taskName) || sub.deprecated) continue;
        options.push({
          name: taskName,
          label: sub.label,
          description: sub.description,
          icon: sub.icon,
          category: category.label,
          categoryIcon: category.icon,
          categoryColor: category.color,
        });
      }
    }
    return options.sort((a, b) => a.category.localeCompare(b.category) || a.label.localeCompare(b.label));
  }, [taskDefinitions, blockedTypes]);

  const actionLabelFor = (taskType?: string) => {
    if (!taskType) return '';
    return actionOptions.find((o) => o.name === taskType)?.label ?? createTaskLabel(taskType);
  };

  const updateSubTask = (index: number, patch: Partial<SubTask>) => {
    // Spread the original entry so YAML-authored keys (if / depends_on / …)
    // survive edits made through this editor. Carry the uid to the copied
    // object so the row's React key (expansion, focus) survives the edit.
    const oldSub = value[index];
    const nextSub = { ...oldSub, ...patch };
    if (oldSub && typeof oldSub === 'object') {
      const uid = uidMap.current.get(oldSub);
      if (uid) uidMap.current.set(nextSub, uid);
    }
    onChange(value.map((sub, i) => (i === index ? nextSub : sub)));
  };

  const updateSubTaskParam = (index: number, fieldName: string, fieldValue: any, clearFields: string[] = []) => {
    const current = value[index] ?? {};
    const params = { ...(current.params ?? {}) };
    for (const field of clearFields) delete params[field];
    if (fieldValue === undefined) {
      delete params[fieldName];
    } else {
      params[fieldName] = fieldValue;
    }
    updateSubTask(index, { params });
  };

  const nextUniqueId = () => {
    const existing = new Set(value.map((sub) => sub?.id).filter(Boolean));
    let n = value.length + 1;
    while (existing.has(`task_${n}`)) n += 1;
    return `task_${n}`;
  };

  const handleAdd = () => {
    const newSub: SubTask = { id: nextUniqueId(), type: '', params: {} };
    const uid = `${testIdPrefix}-sub-${uidCounter.current++}`;
    uidMap.current.set(newSub, uid);
    onChange([...value, newSub]);
    setExpandedIds((prev) => [...prev, uid]);
  };

  const handleDelete = (index: number) => {
    const uid = uids[index];
    setExpandedIds((prev) => prev.filter((id) => id !== uid));
    onChange(value.filter((_, i) => i !== index));
  };

  // Auto-filled params alone (defaults / workflow account) don't warrant a
  // "configured parameters will be lost" confirmation.
  const hasUserParams = (sub: SubTask | undefined): boolean => {
    const params = sub?.params ?? {};
    if (Object.keys(params).length === 0) return false;
    const definition = sub?.type ? taskDefinitions.find((d: any) => d.name === sub.type) : null;
    const initial = buildInitialSubTaskParams(definition, accountId);
    const keys = Object.keys(params);
    // Per-key compare: a cleared-then-retyped field re-enters at the end, so key order can differ
    return keys.length !== Object.keys(initial).length || keys.some((k) => JSON.stringify(params[k]) !== JSON.stringify(initial[k]));
  };

  const requestDelete = (index: number) => {
    if (!hasUserParams(value[index])) {
      handleDelete(index);
    } else {
      setPendingDelete(index);
    }
  };

  const applyTypeChange = (index: number, newType: string) => {
    const definition = taskDefinitions.find((d: any) => d.name === newType);
    updateSubTask(index, { type: newType, params: buildInitialSubTaskParams(definition, accountId) });
  };

  const requestTypeChange = (index: number, newType: string) => {
    const current = value[index] ?? {};
    if (newType === current.type) return;
    if (hasUserParams(current)) {
      setPendingTypeChange({ index, newType });
    } else {
      applyTypeChange(index, newType);
    }
  };

  // Reordered entries keep their object identity, so uids travel with them —
  // no manual bookkeeping needed.
  const handleReorder = (next: SubTask[]) => {
    onChange(next);
  };

  const countSubTaskErrors = (index: number): number => {
    const prefix = `tasks[${index}].`;
    return Object.keys(errors).filter((key) => key.startsWith(prefix)).length;
  };

  const paramErrorsFor = (index: number): Record<string, string> => {
    const prefix = `tasks[${index}].params.`;
    const sliced: Record<string, string> = {};
    for (const [key, message] of Object.entries(errors)) {
      if (key.startsWith(prefix)) {
        sliced[key.slice(prefix.length)] = message;
      }
    }
    return sliced;
  };

  const renderSubTaskBody = (sub: SubTask, index: number) => {
    const taskDefinition = sub.type ? taskDefinitions.find((d: any) => d.name === sub.type) : null;
    const idError = validateTaskId(sub.id ?? '') || errors[`tasks[${index}].id`] || '';
    const typeError = errors[`tasks[${index}].type`] || '';

    return (
      <Box sx={{ pt: 1 }}>
        <Box sx={{ mb: 2 }}>
          <StableTextField
            fieldName='id'
            value={sub.id ?? ''}
            onChange={(_field, newValue) => updateSubTask(index, { id: newValue })}
            label='Name'
            isRequired
            placeholder='e.g. process_item'
            disabled={viewOnlyMode}
            error={idError}
          />
        </Box>

        <Box sx={{ mb: 2, display: 'flex', alignItems: 'flex-start', gap: 1.5, flexWrap: 'wrap' }}>
          <Typography
            sx={{
              fontSize: 'var(--ds-text-small)',
              fontWeight: 'var(--ds-font-weight-medium)',
              color: ds.gray[700],
              minWidth: '110px',
              maxWidth: '110px',
              pt: 1,
            }}
          >
            Action<span style={{ color: ds.red[500] }}> *</span>
          </Typography>
          <Box sx={{ flex: '1 1 240px', minWidth: '200px' }}>
            <Autocomplete
              size='small'
              options={actionOptions}
              groupBy={(option) => option.category}
              getOptionLabel={(option) => option.label}
              isOptionEqualToValue={(option, selected) => option.name === selected.name}
              value={
                actionOptions.find((o) => o.name === sub.type) ??
                (sub.type ? { name: sub.type, label: createTaskLabel(sub.type), icon: null, category: '' } : null)
              }
              onChange={(_e, newValue) => {
                if (newValue) requestTypeChange(index, newValue.name);
              }}
              filterOptions={(options, state) => {
                const q = state.inputValue.trim().toLowerCase();
                if (!q) return options;
                return options.filter(
                  (o) => o.label.toLowerCase().includes(q) || o.name.toLowerCase().includes(q) || (o.description ?? '').toLowerCase().includes(q)
                );
              }}
              disabled={viewOnlyMode}
              disableClearable={!!sub.type}
              ListboxProps={{ sx: { py: 0 } }}
              renderGroup={(params) => {
                const groupOption = actionOptions.find((o) => o.category === params.group);
                return (
                  <li key={params.key}>
                    <Box
                      sx={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: 1,
                        px: 1.5,
                        py: 0.75,
                        position: 'sticky',
                        top: 0,
                        zIndex: 1,
                        backgroundColor: 'var(--ds-background-100, white)',
                        borderBottom: `1px solid ${ds.gray[200]}`,
                      }}
                      data-testid={`${testIdPrefix}-subtask-action-group-${params.group}`}
                    >
                      {groupOption?.categoryIcon && (
                        <Box
                          sx={{
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            width: 24,
                            height: 24,
                            borderRadius: 'var(--ds-radius-md)',
                            backgroundColor: `color-mix(in srgb, ${groupOption.categoryColor} 10%, transparent)`,
                            border: `1px solid color-mix(in srgb, ${groupOption.categoryColor} 35%, transparent)`,
                            flexShrink: 0,
                          }}
                        >
                          {renderCategoryOrTaskIcon(groupOption.categoryIcon, params.group, 14)}
                        </Box>
                      )}
                      <Typography
                        sx={{
                          fontSize: 'var(--ds-text-small)',
                          fontWeight: 'var(--ds-font-weight-semibold)',
                          fontFamily: 'var(--ds-font-display)',
                          color: 'var(--ds-brand-500)',
                        }}
                      >
                        {params.group}
                      </Typography>
                    </Box>
                    <ul style={{ padding: 0 }}>{params.children}</ul>
                  </li>
                );
              }}
              renderOption={(props, option) => (
                <li {...props} key={option.name} style={{ ...(props as any).style, paddingTop: 4, paddingBottom: 4 }}>
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, minWidth: 0 }}>
                    <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'center', width: 24, height: 24, flexShrink: 0 }}>
                      {option.icon && renderCategoryOrTaskIcon(option.icon, option.label, 20)}
                    </Box>
                    <Box sx={{ minWidth: 0 }}>
                      <Typography
                        sx={{
                          fontSize: 'var(--ds-text-small)',
                          fontWeight: 'var(--ds-font-weight-semibold)',
                          fontFamily: 'var(--ds-font-display)',
                          color: 'var(--ds-brand-500)',
                          lineHeight: 1.3,
                        }}
                      >
                        {option.label}
                      </Typography>
                      {option.description && (
                        <Typography noWrap sx={{ fontSize: 'var(--ds-text-caption)', color: 'var(--ds-gray-600)', lineHeight: 1.3 }}>
                          {option.description}
                        </Typography>
                      )}
                    </Box>
                  </Box>
                </li>
              )}
              renderInput={(params) => {
                const selectedIcon = actionOptions.find((o) => o.name === sub.type)?.icon;
                return (
                  <TextField
                    {...params}
                    placeholder='Select action'
                    error={!!typeError}
                    helperText={typeError || undefined}
                    InputProps={{
                      ...params.InputProps,
                      startAdornment: selectedIcon ? (
                        <InputAdornment position='start' sx={{ ml: 0.5, mr: 0 }}>
                          <Box
                            data-testid={`${testIdPrefix}-subtask-${index}-selected-action-icon`}
                            sx={{ display: 'flex', alignItems: 'center', justifyContent: 'center', width: 20, height: 20, flexShrink: 0 }}
                          >
                            {renderCategoryOrTaskIcon(selectedIcon, actionLabelFor(sub.type), 18)}
                          </Box>
                        </InputAdornment>
                      ) : (
                        params.InputProps.startAdornment
                      ),
                    }}
                  />
                );
              }}
              data-testid={`${testIdPrefix}-subtask-${index}-type-picker`}
            />
          </Box>
        </Box>

        {sub.type && taskDefinition && (
          <SubTaskParamForm
            taskDefinition={taskDefinition}
            values={sub.params ?? {}}
            onChange={(fieldName, fieldValue, clearFields) => updateSubTaskParam(index, fieldName, fieldValue, clearFields)}
            errors={paramErrorsFor(index)}
            disabled={viewOnlyMode}
            extraSuggestions={extraSuggestions}
            previousTasks={previousTasks}
            workflowInputs={workflowInputs}
            workflowConfigs={workflowConfigs}
          />
        )}
      </Box>
    );
  };

  const renderSubTaskRow = (sub: SubTask, index: number, dragHandleProps: any) => {
    const uid = uids[index];
    const errorCount = countSubTaskErrors(index);
    const label = sub.id || `Sub-task ${index + 1}`;
    return (
      <Accordion
        density='sm'
        items={[
          {
            id: uid,
            label,
            description: sub.type ? actionLabelFor(sub.type) : 'No action selected',
            icon: (
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <span
                  {...(viewOnlyMode ? {} : dragHandleProps)}
                  onClick={(e) => e.stopPropagation()}
                  style={{ display: 'inline-flex', alignItems: 'center', ...(viewOnlyMode ? {} : dragHandleProps.style) }}
                >
                  <DragIndicator sx={{ fontSize: 16, color: ds.gray[400] }} />
                </span>
                <SubTaskIconBadge taskType={sub.type} label={label} />
              </span>
            ),
            meta: (
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }} onClick={(e) => e.stopPropagation()}>
                {errorCount > 0 && <Label tone='critical'>{`${errorCount} ${errorCount === 1 ? 'issue' : 'issues'}`}</Label>}
                {!viewOnlyMode && (
                  <IconButton
                    size='small'
                    onClick={() => requestDelete(index)}
                    aria-label={`Delete sub-task ${sub.id || index + 1}`}
                    data-testid={`${testIdPrefix}-subtask-${index}-delete-btn`}
                  >
                    <SafeIcon src={DeleteIconRed} alt='delete' width={16} height={16} />
                  </IconButton>
                )}
              </span>
            ),
            body: renderSubTaskBody(sub, index),
          },
        ]}
        expandedIds={expandedIds}
        onExpandedChange={setExpandedIds}
      />
    );
  };

  return (
    <Box>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 1 }}>
        <Typography sx={{ fontSize: 'var(--ds-text-body)', fontWeight: 'var(--ds-font-weight-medium)', color: ds.gray[700] }}>
          Sub-tasks ({value.length})
        </Typography>
        {!viewOnlyMode && (
          <Button tone='secondary' size='sm' icon={<Add sx={{ fontSize: 16 }} />} onClick={handleAdd} data-testid={`${testIdPrefix}-add-subtask-btn`}>
            Add sub-task
          </Button>
        )}
      </Box>
      <Typography sx={{ fontSize: 'var(--ds-text-caption)', color: ds.gray[400], mb: 1 }}>{copy.helperText}</Typography>
      {errors['tasks'] && <Typography sx={{ fontSize: 'var(--ds-text-caption)', color: 'var(--ds-red-700)', mb: 1 }}>{errors['tasks']}</Typography>}

      {value.length === 0 ? (
        <Box
          sx={{
            border: `2px dashed ${ds.gray[300]}`,
            borderRadius: ds.radius.md,
            p: 3,
            textAlign: 'center',
          }}
        >
          <Typography sx={{ fontSize: 'var(--ds-text-small)', color: ds.gray[400], mb: 1 }}>{copy.emptyStateText}</Typography>
          {!viewOnlyMode && (
            <Button tone='primary' size='sm' onClick={handleAdd} data-testid={`${testIdPrefix}-empty-add-subtask-btn`}>
              Add your first sub-task
            </Button>
          )}
        </Box>
      ) : (
        <Box sx={{ border: `1px solid ${ds.gray[200]}`, borderRadius: ds.radius.md, overflow: 'hidden' }}>
          <ReorderableList
            items={value}
            onReorder={handleReorder}
            getItemKey={(_item, index) => uids[index]}
            renderItem={renderSubTaskRow}
            disabled={viewOnlyMode}
            getDragLabel={(item, index) => item.id || `Sub-task ${index + 1}`}
            helperText=''
          />
        </Box>
      )}

      <Modal
        open={pendingDelete !== null}
        handleClose={() => setPendingDelete(null)}
        title='Delete sub-task?'
        confirmText='Delete'
        onConfirm={() => {
          if (pendingDelete !== null) handleDelete(pendingDelete);
          setPendingDelete(null);
        }}
      >
        <Typography sx={{ fontSize: 'var(--ds-text-body)', color: ds.gray[700] }}>
          “{pendingDelete !== null ? value[pendingDelete]?.id || `Sub-task ${pendingDelete + 1}` : ''}” has configured parameters that will be lost.
        </Typography>
      </Modal>

      <Modal
        open={pendingTypeChange !== null}
        handleClose={() => setPendingTypeChange(null)}
        title='Change action?'
        confirmText='Change'
        onConfirm={() => {
          if (pendingTypeChange) applyTypeChange(pendingTypeChange.index, pendingTypeChange.newType);
          setPendingTypeChange(null);
        }}
      >
        <Typography sx={{ fontSize: 'var(--ds-text-body)', color: ds.gray[700] }}>
          Changing the action clears this sub-task’s configured parameters.
        </Typography>
      </Modal>
    </Box>
  );
};

export default SubTasksEditor;
