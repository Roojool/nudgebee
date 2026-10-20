describe('ADMIN_OVERVIEW_COUNTS (api1/admin-overview)', () => {
  const loadQuery = (oss: boolean): string => {
    let query = '';
    jest.isolateModules(() => {
      jest.doMock('@hooks/useBCortexEnabled', () => ({ isOSSDeploymentMode: () => oss }));
      jest.doMock('@lib/HttpService', () => ({ queryGraphQL: jest.fn() }));
      query = require('@api1/admin-overview').ADMIN_OVERVIEW_COUNTS;
    });
    return query;
  };

  // The gateway refuses the whole batched document when any field has no route,
  // and customroles_list is an Enterprise-only action.
  it('leaves the Enterprise customroles_list field out on OSS', () => {
    const query = loadQuery(true);
    expect(query).not.toContain('customroles_list');
    expect(query).toContain('users_aggregate_by_tenant');
    expect(query.trim().endsWith('}')).toBe(true);
  });

  it('asks for custom roles on an enterprise build', () => {
    expect(loadQuery(false)).toContain('roles: customroles_list { roles { id } }');
  });
});
