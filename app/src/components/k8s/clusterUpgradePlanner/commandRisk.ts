// Which plan-step commands change the cluster, and therefore have to pause for a
// human before they run.
//
// The Run button on an upgrade-plan step posts the command straight to
// upgrade_execute_command, which validates it against its own allow-lists in
// api-server/services/k8s_upgrade/service.go (kubectlAllowedCommands /
// awsAllowedCommands). Those lists permit read-only inspection AND a handful of
// mutating verbs — `kubectl drain` and `kubectl scale` among them — so "the
// server accepted it" is not the same as "it was safe to click".
//
// This module lists the READ-ONLY half instead of the mutating half, deliberately.
// Listing the mutating verbs would mean that a verb added to the server's
// allow-list and missed here executes with no prompt — the gate fails open, and
// silently. Listing the read-only verbs makes the same drift fail closed: the new
// verb prompts until someone decides it shouldn't. The cost of being wrong in
// this direction is one dialog; in the other it is an unannounced `drain`.
const READ_ONLY_KUBECTL_SUBCOMMANDS = new Set(['get', 'describe', 'logs', 'top', 'version', 'cluster-info']);

const READ_ONLY_AWS_SUBCOMMANDS: Record<string, Set<string>> = {
  eks: new Set([
    'describe-cluster',
    'list-clusters',
    'describe-update',
    'list-updates',
    'describe-nodegroup',
    'list-nodegroups',
    'get-token',
    'describe-addon-versions',
    'describe-addon',
    'list-addons',
  ]),
};

// Parsing mirrors the server exactly — strings.Fields() then a positional read of
// parts[1] (kubectl) or parts[1]/parts[2] (aws). Deliberately NOT a smarter parse
// that skips leading flags: if the two disagree about which word is the verb, the
// server's reading is the one that executes, and a cleverer client would classify
// a command the server runs as something else.
const fields = (command: string): string[] => command.trim().split(/\s+/).filter(Boolean);

/**
 * True when running `command` could change cluster state.
 *
 * Anything not positively recognised as read-only returns true: the caller uses
 * this to decide whether to ask first, and asking about something harmless is a
 * far smaller cost than running something destructive silently.
 */
export const isMutatingCommand = (command: string): boolean => {
  const parts = fields(command);
  if (parts.length === 0) {
    return true;
  }

  // Commands the server rejects before executing anything don't need a prompt —
  // confirming them would only produce a validation error on the other side.
  if (parts[0] === 'kubectl') {
    return parts.length < 2 ? false : !READ_ONLY_KUBECTL_SUBCOMMANDS.has(parts[1]);
  }

  if (parts[0] === 'aws') {
    if (parts.length < 3) {
      return false;
    }
    const readOnlyForService = READ_ONLY_AWS_SUBCOMMANDS[parts[1]];
    return readOnlyForService ? !readOnlyForService.has(parts[2]) : true;
  }

  return true;
};

/**
 * The one-line consequence shown above a mutating command in the confirm dialog.
 * Generic fallback rather than a per-verb essay — the command itself is on screen
 * directly beneath it, so this only has to say what kind of change is coming.
 */
export const mutatingCommandWarning = (command: string): string => {
  const parts = fields(command);
  const subcommand = parts[0] === 'kubectl' ? parts[1] : parts[2];

  switch (subcommand) {
    case 'drain':
      return 'This evicts every pod from the node and marks it unschedulable. Workloads without spare capacity elsewhere will be unavailable until they reschedule.';
    case 'cordon':
      return 'This marks the node unschedulable. Running pods stay put, but nothing new will be placed on it.';
    case 'uncordon':
      return 'This makes the node schedulable again.';
    case 'scale':
      return 'This changes the replica count. Scaling to a lower number stops the pods above it.';
    case 'patch':
      return 'This edits the live resource definition.';
    case 'rollout':
      return 'This acts on the live rollout — pods will be replaced or reverted.';
    case 'update-nodegroup-config':
      return 'This changes the live node group configuration in AWS.';
    default:
      return 'This changes the state of your cluster.';
  }
};
