import { useEffect, useState } from 'react';
import apiKubernetes from '@api1/kubernetes';
import MarkDowns from '@shared/viewers/MarkDowns';

const EMPTY_KNOWLEDGE_BASE = [];

const KnowledgeBase = ({ troubleShootingEvent, preLoadedKnowledgeBase = EMPTY_KNOWLEDGE_BASE }) => {
  const [knowledgeBase, setKnowledgeBase] = useState(preLoadedKnowledgeBase);
  const [, setLoading] = useState(false);

  useEffect(() => {
    if (preLoadedKnowledgeBase?.length > 0) {
      return;
    }
    let cancelled = false;

    const fetchKB = async () => {
      if (!troubleShootingEvent?.aggregation_key) {
        return;
      }

      try {
        setLoading(true);
        const res = await apiKubernetes.getKnowledgeBase(troubleShootingEvent.aggregation_key);
        if (cancelled) {
          return;
        }
        if (res?.data) {
          setKnowledgeBase(res.data);
        }
      } catch (e) {
        console.error(e);
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    };

    fetchKB();

    return () => {
      cancelled = true;
    };
  }, [troubleShootingEvent, preLoadedKnowledgeBase]);

  if (!knowledgeBase.length) {
    return null;
  }

  return (
    <>
      {knowledgeBase.map((base, index) => {
        let data = '';
        if (base.description) {
          data += base.description;
        }
        if (base.impact) {
          data += '\n\n' + base.impact;
        }
        if (base.diagnosis) {
          data += '\n\n' + base.diagnosis;
        }
        if (base.mitigation) {
          data += '\n\n' + base.mitigation;
        }

        return <MarkDowns key={index} data={data} sx={{ width: '100%' }} />;
      })}
    </>
  );
};

export default KnowledgeBase;
