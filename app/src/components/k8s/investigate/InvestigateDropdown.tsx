import React, { useState, useEffect } from 'react';
import k8sApi from '@api1/kubernetes';
import { useRouter } from 'next/router';
import { Select, type SelectOption } from '@ui/Select';
import { Box, Typography } from '@mui/material';
import { ds } from 'src/utils/colors';
import dayjs from 'dayjs';
import { parseDateValue } from '@shared/format/Datetime';
import relativeTime from 'dayjs/plugin/relativeTime';

dayjs.extend(relativeTime);

interface InvestigateDropdownProps {
  query: any;
  inputMaxWidth: string;
  subjectName: string;
  subjectNamespace: string;
  resetStateWhenItemSelected: () => void;
  // When provided, the dropdown renders these options instead of fetching
  // same-subject events itself (used for the same-incident group, #34655).
  optionsOverride?: SelectOption[];
  // Events to omit from the fetched list — e.g. incident-group members, so
  // "Related Events" means "on this subject but outside this incident".
  excludeIds?: string[];
  title?: string;
  placeholder?: string;
}

const InvestigateDropdown: React.FC<InvestigateDropdownProps> = ({
  query,
  inputMaxWidth,
  subjectName,
  subjectNamespace,
  resetStateWhenItemSelected,
  optionsOverride,
  excludeIds,
  title = 'Other events here',
  placeholder = 'No related events found',
}) => {
  const router = useRouter();
  const [fetchedOptions, setFetchedOptions] = useState<SelectOption[]>([]);
  const accountId = router.query.accountId;

  useEffect(() => {
    if (optionsOverride || !query.id || !accountId) {
      return;
    }
    const queryParams: any = {};

    if (subjectName) {
      queryParams.subject_name = subjectName;
    }
    if (subjectNamespace) {
      queryParams.subject_namespace = subjectNamespace;
    }
    queryParams.account_id = accountId;
    queryParams.finding_type = 'issue';

    let cancelled = false;
    k8sApi
      .getK8sEventsName(10, 0, queryParams)
      .then((res: any) => {
        if (cancelled) {
          return;
        }
        const events = res?.data?.events;
        const options: SelectOption[] = (Array.isArray(events) ? events : [])
          .filter((item: any) => !excludeIds?.includes(String(item.id)))
          .map((item: any) => ({
            value: String(item.id),
            label: item.title,
            // The server sends no offset, and dayjs reads such a string as local —
            // every entry was off by the viewer's UTC offset.
            subtext: dayjs(parseDateValue(item.starts_at)).fromNow(),
          }));
        setFetchedOptions(options);
      })
      .catch((e) => {
        if (!cancelled) {
          console.error(e);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [query, accountId, optionsOverride, excludeIds]);

  const optionsData = optionsOverride ?? fetchedOptions;

  const handleChange = (next: string) => {
    if (next) {
      resetStateWhenItemSelected();
      router.push(`/investigate?id=${next}&accountId=${router.query.accountId}`);
    }
  };

  const selectedId = router.query.id ? String(router.query.id) : null;
  const selectedValue = optionsData.find((o) => o.value === selectedId) ? selectedId : null;

  return (
    <Box>
      {/* Rendered inside the sidebar's Related section, which carries the heading;
          this is just the field's label. */}
      <Typography sx={{ color: ds.gray[600], fontSize: ds.text.small, mb: ds.space[1] }}>{title}</Typography>
      <Select
        options={optionsData}
        onChange={handleChange}
        value={selectedValue}
        minWidth={inputMaxWidth ?? '100%'}
        size='sm'
        id='investigate-other-events'
        placeholder={placeholder}
      />
    </Box>
  );
};

export default InvestigateDropdown;
