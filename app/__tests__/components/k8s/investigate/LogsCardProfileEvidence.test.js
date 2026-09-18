import LogsCard from '@components/k8s/investigate/cards/LogsCard';

// unzipData would choke on the fake payloads below, and what is under test is
// which evidence the card claims, not how it renders a log file.
jest.mock('@components/k8s/investigate/cards/util', () => ({
  base64Converter: (data) => data,
  unzipData: async () => 'a log line\nanother log line',
}));

// The four shapes a profile actually arrives in, taken from completed
// pod_profiler runs: Go pprof, a flamegraph from py-spy and from
// async-profiler, and a jcmd heap histogram. Only the first ends in pprof.gz,
// which is why matching the extension was not enough.
const profileEvidence = (filename) => ({
  type: 'gz',
  filename,
  data: 'BASE64',
  additional_info: { actual_action_name: 'pod_profiler' },
});

const logEvidence = {
  type: 'gz',
  filename: '/tmp/pod-log.log.gz',
  data: 'BASE64',
  additional_info: { pod_name: 'web-0' },
};

describe('LogsCard and profile evidence', () => {
  it.each([
    ['go pprof', '/tmp/agent-heapdump-4110012-1.pprof.gz'],
    ['py-spy flamegraph', '/tmp/agent-flamegraph-2882457-1.svg.gz'],
    ['async-profiler flamegraph', '/tmp/agent-flamegraph-10684-1.html.gz'],
    ['jcmd heap histogram', '/tmp/agent-heaphistogram-12275-1.txt.gz'],
  ])('does not claim a %s as a log file', async (_name, filename) => {
    const card = new LogsCard();

    await expect(card.canRenderContent([profileEvidence(filename)], {})).resolves.toBe(false);
  });

  it('still claims real logs when a profile sits beside them', async () => {
    const card = new LogsCard();

    await expect(card.canRenderContent([profileEvidence('/tmp/agent-flamegraph-1.svg.gz'), logEvidence], {})).resolves.toBe(true);
  });

  // The card renders as soon as one genuine log is present, and then parses
  // every .gz it was handed — so the profile has to be dropped here too, or it
  // is unzipped and split into "log lines".
  it('leaves the profile out of the logs it renders', async () => {
    const card = new LogsCard();

    await card.canRenderContent([profileEvidence('/tmp/agent-flamegraph-1.svg.gz'), logEvidence], {});

    const fileNames = card.logs.map((log) => log.fileName);
    expect(fileNames).toContain('/tmp/pod-log.log.gz');
    expect(fileNames).not.toContain('/tmp/agent-flamegraph-1.svg.gz');
  });
});
