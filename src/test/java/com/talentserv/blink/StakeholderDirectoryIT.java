package com.talentserv.blink;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;

import com.talentserv.blink.service.RoleCatalog;

@SpringBootTest
class StakeholderDirectoryIT {

    @Autowired
    private RoleCatalog roleCatalog;

    @Test
    void loadsPeopleFromStakeholdersYaml() {
        assertThat(roleCatalog.roles()).hasSize(14);
        assertThat(roleCatalog.roles().getFirst().roleCode()).isEqualTo("product_owner");
        assertThat(roleCatalog.roles().getFirst().defaultName()).isNull();
        assertThat(roleCatalog.roles().getFirst().defaultEmail()).isNull();
        assertThat(roleCatalog.roles()).extracting(role -> role.defaultName()).doesNotContain("Rohit Naik", "Atul Maurya");
        assertThat(roleCatalog.nameFor("backend_developer")).isEqualTo("Backend Developer");
    }
}
