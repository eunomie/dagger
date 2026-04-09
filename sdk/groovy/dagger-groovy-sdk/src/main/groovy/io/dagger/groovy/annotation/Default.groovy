package io.dagger.groovy.annotation

import java.lang.annotation.ElementType
import java.lang.annotation.Retention
import java.lang.annotation.RetentionPolicy
import java.lang.annotation.Target

@Target(ElementType.PARAMETER)
@Retention(RetentionPolicy.SOURCE)
@interface Default {
    String value()
}
