package com.talentserv.blink.service;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;

class RoleCatalogTest {

    @Test
    void displayNameTitleCasesRoleCodes() {
        assertThat(RoleCatalog.displayName("product_owner")).isEqualTo("Product Owner");
        assertThat(RoleCatalog.displayName("ux_designer")).isEqualTo("UX Designer");
        assertThat(RoleCatalog.displayName("dba")).isEqualTo("DBA");
        assertThat(RoleCatalog.displayName("qa_lead")).isEqualTo("QA Lead");
        assertThat(RoleCatalog.displayName("sre")).isEqualTo("SRE");
    }

    @Test
    void loadsPeopleFromStakeholdersYaml() {
        RoleCatalog catalog = new RoleCatalog();
        assertThat(catalog.roles()).hasSize(14);
        assertThat(catalog.roles().getFirst().roleCode()).isEqualTo("product_owner");
        assertThat(catalog.roles().getFirst().defaultName()).isNull();
        assertThat(catalog.roles().getFirst().defaultEmail()).isNull();
        assertThat(catalog.roles()).extracting(role -> role.defaultName()).doesNotContain("Rohit Naik", "Atul Maurya");
        assertThat(catalog.nameFor("backend_developer")).isEqualTo("Backend Developer");
    }
}
