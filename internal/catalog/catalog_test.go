package catalog

import "testing"

const csdl = `<?xml version="1.0" encoding="utf-8"?>
<edmx:Edmx Version="4.0" xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx">
 <edmx:DataServices>
  <Schema Namespace="Sungero" xmlns="http://docs.oasis-open.org/odata/ns/edm">
   <EntityType Name="Entity"><Key><PropertyRef Name="Id"/></Key><Property Name="Id" Type="Edm.Int64" Nullable="false"/></EntityType>
   <EntityType Name="Contract" BaseType="Sungero.Entity">
     <Property Name="Name" Type="Edm.String"/><Property Name="TotalAmount" Type="Edm.Double"/>
     <NavigationProperty Name="Counterparty" Type="Sungero.Company"/>
     <NavigationProperty Name="Versions" Type="Collection(Sungero.Version)"/>
   </EntityType>
   <EntityType Name="Company" BaseType="Sungero.Entity"><Property Name="TIN" Type="Edm.String"/></EntityType>
   <EntityType Name="Version"><Property Name="Number" Type="Edm.Int32"/></EntityType>
  </Schema>
  <Schema Namespace="Default" xmlns="http://docs.oasis-open.org/odata/ns/edm">
   <EntityContainer Name="Container">
    <EntitySet Name="IContracts" EntityType="Sungero.Contract"/>
    <EntitySet Name="ICompanies" EntityType="Sungero.Company"/>
    <EntitySet Name="IContractCategories" EntityType="Sungero.Entity"/>
   </EntityContainer>
  </Schema>
 </edmx:DataServices>
</edmx:Edmx>`

func TestParseFindDescribe(t *testing.T) {
	c, err := Parse([]byte(csdl))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sets) != 3 {
		t.Fatalf("наборов %d", len(c.Sets))
	}
	if got := c.Find("договоры", 5); len(got) != 2 || got[0].Name != "IContracts" {
		t.Errorf("поиск по-русски: %+v", got)
	}
	if got := c.Find("compan", 5); len(got) != 1 || got[0].Name != "ICompanies" {
		t.Errorf("поиск по-английски: %+v", got)
	}
	if got := c.Find("контрагент", 5); len(got) != 1 || got[0].Name != "ICompanies" {
		t.Errorf("контрагент: %+v", got)
	}
	s, props, navs, ok := c.Describe("icontracts")
	if !ok || s.Name != "IContracts" || len(props) != 3 || props[0].Name != "Id" || len(navs) != 2 {
		t.Fatalf("describe: %v %+v %+v", ok, props, navs)
	}
	if navs[0].Name != "Counterparty" || navs[0].Type != "Company" || navs[1].Type != "Version[]" {
		t.Errorf("ссылки: %+v", navs)
	}
	if _, _, _, ok := c.Describe("Nope"); ok {
		t.Error("несуществующий набор")
	}
	if _, err := Parse([]byte("<x/>")); err == nil {
		t.Error("пустые метаданные должны давать ошибку")
	}
}
