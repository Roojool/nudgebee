import TracesCard, { isTracesCardId } from '@components/k8s/investigate/cards/TracesCard';
import CorrespondingEvents from '@components/k8s/investigate/cards/CorrespondingEvents';

// The card body fetches and renders a K8s events table; these assertions are about
// canRenderContent, so keep the module graph out of it.
jest.mock('@components/events/KubernetesEvents', () => ({ __esModule: true, default: () => null }));
jest.mock('@api1/kubernetes', () => ({ __esModule: true, default: { relayForwardRequest: jest.fn() } }));

// Shape of an `api_traces_enricher` evidence as the api-server stores it: raw ClickHouse
// span columns, with each span naming both of its ends under SpanAttributes.
const span = (source, destination) => ({
  TraceId: '17b6ab73546fffbb4bcebf99dc89e880',
  SpanId: '24578080047015802',
  ServiceName: source.workload,
  ResourceAttributes: { 'service.name': source.workload, 'cloud.region': 'us-central1' },
  SpanAttributes: {
    'source.workload_namespace': source.namespace,
    'source.workload_name': source.workload,
    'destination.workload_namespace': destination.namespace,
    'destination.workload_name': destination.workload,
  },
});

const tracesEvidence = (spans) => ({
  type: 'json',
  data: JSON.stringify({ name: 'api_traces_enricher', data: spans }),
});

const event = { id: 'event-under-test', subject_namespace: 'shop', subject_name: 'cart' };

const evidenceList = (items, canTracesRender) => {
  const list = [...items];
  // investigate.jsx hangs this flag off the evidence array once a TracesCard rendered.
  list.canTracesRender = canTracesRender;
  return list;
};

describe('CorrespondingEvents.canRenderContent', () => {
  it('renders when a traces card rendered and the spans name their workloads', async () => {
    const card = new CorrespondingEvents();
    const evidence = evidenceList(
      [tracesEvidence([span({ namespace: 'shop', workload: 'frontend' }, { namespace: 'shop', workload: 'cart' })])],
      true
    );

    await expect(card.canRenderContent(evidence, event)).resolves.toBe(true);
    expect(card.namespaces).toEqual(['shop']);
    expect(card.workloadNames).toEqual(expect.arrayContaining(['frontend', 'cart']));
  });

  it('stays closed until a traces card has rendered', async () => {
    const card = new CorrespondingEvents();
    const evidence = evidenceList(
      [tracesEvidence([span({ namespace: 'shop', workload: 'frontend' }, { namespace: 'shop', workload: 'cart' })])],
      false
    );

    await expect(card.canRenderContent(evidence, event)).resolves.toBe(false);
  });

  it('does not throw on spans that carry neither SpanAttributes nor ResourceAttributes', async () => {
    const card = new CorrespondingEvents();
    const evidence = evidenceList([tracesEvidence([{ TraceId: 'abc', SpanId: '1' }])], true);

    await expect(card.canRenderContent(evidence, event)).resolves.toBe(false);
  });
});

// investigate.jsx opens the gate above by asking isTracesCardId whether the card that
// just rendered was a traces card. It used to compare the id to a bare 'TracesCard'
// instead, which no dynamic card is ever called, so the gate stayed shut.
describe('isTracesCardId', () => {
  it('accepts the id a real TracesCard carries', () => {
    expect(isTracesCardId(new TracesCard({}, {}, 3).id)).toBe(true);
  });

  it('is a prefix match, not the equality check it replaces', () => {
    const id = new TracesCard({}, {}, 3).id;

    expect(id).not.toBe('TracesCard');
    expect(id === 'TracesCard').toBe(false);
  });

  it('rejects other cards and missing ids', () => {
    expect(isTracesCardId('CorrespondingEvents')).toBe(false);
    expect(isTracesCardId(undefined)).toBe(false);
  });

  it('does not claim a differently-named card that merely starts the same', () => {
    expect(isTracesCardId('TracesCardSummary_1')).toBe(false);
  });
});
